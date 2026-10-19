// select-region: a full-screen overlay for picking an area of the screen.
//
// Drag a rectangle on any display. On mouse-up the selection is printed to
// stdout as one line of JSON and the process exits 0:
//
//   {"displayID":1,"h":320,"scale":2,"screenIndex":0,"w":600,"x":412,"y":188}
//
// x, y, w and h are in points, relative to the top-left corner of the display
// the drag started on; multiply by scale for pixels. displayID is the
// CGDirectDisplayID of that display, and screenIndex its position in
// CGGetActiveDisplayList, which is the N in ffmpeg avfoundation's
// "Capture screen N". Esc or right-click cancels: nothing is printed and the
// exit status is 1. "frame":true says this helper supports --frame.
//
// With --frame DISPLAYID X Y W H (points, as printed above), it instead dims
// everything but that area and draws a dotted border just outside it, so the
// pointer can be kept inside it while recording. None of it is drawn inside
// the area, it ignores the mouse, and it stays up until stdin closes or the
// process is killed.
//
// Build on macOS (needs the Xcode Command Line Tools):
//
//   swiftc -O helper/select-region.swift -o bin/select-region

import AppKit

struct Selection: Encodable {
    let x: Double
    let y: Double
    let w: Double
    let h: Double
    let scale: Double
    let displayID: UInt32
    let screenIndex: Int
    let frame = true
}

extension NSScreen {
    var displayID: UInt32 {
        (deviceDescription[NSDeviceDescriptionKey("NSScreenNumber")] as? NSNumber)?.uint32Value ?? 0
    }
}

/// The display's position in CGGetActiveDisplayList. ffmpeg's avfoundation
/// input numbers its "Capture screen N" devices in this order, which can
/// differ from NSScreen.screens.
func activeDisplayIndex(of id: CGDirectDisplayID) -> Int? {
    var count: UInt32 = 0
    guard CGGetActiveDisplayList(0, nil, &count) == .success, count > 0 else { return nil }
    var ids = [CGDirectDisplayID](repeating: 0, count: Int(count))
    guard CGGetActiveDisplayList(count, &ids, &count) == .success else { return nil }
    return ids.prefix(Int(count)).firstIndex(of: id)
}

/// Borderless windows refuse key status by default; the overlay needs it to
/// receive Esc.
final class OverlayWindow: NSWindow {
    override var canBecomeKey: Bool { true }
    override var canBecomeMain: Bool { true }
}

/// Dims one display and lets the user drag out a rectangle on it.
final class SelectionView: NSView {
    private let screen: NSScreen
    private let onFinish: (NSRect, NSScreen) -> Void
    private let onCancel: () -> Void
    private var anchor: NSPoint?
    private var current: NSPoint?

    /// Drags smaller than this many points are treated as stray clicks.
    private let minSize: CGFloat = 4
    private let escKeyCode: UInt16 = 53

    init(screen: NSScreen, onFinish: @escaping (NSRect, NSScreen) -> Void, onCancel: @escaping () -> Void) {
        self.screen = screen
        self.onFinish = onFinish
        self.onCancel = onCancel
        super.init(frame: NSRect(origin: .zero, size: screen.frame.size))
        // .activeAlways keeps the crosshair even if activation was refused.
        addTrackingArea(NSTrackingArea(
            rect: .zero,
            options: [.activeAlways, .inVisibleRect, .mouseMoved, .mouseEnteredAndExited, .cursorUpdate],
            owner: self
        ))
    }

    required init?(coder: NSCoder) { fatalError("init(coder:) is not used") }

    override var acceptsFirstResponder: Bool { true }
    override func acceptsFirstMouse(for event: NSEvent?) -> Bool { true }

    // MARK: Cursor

    override func cursorUpdate(with event: NSEvent) { NSCursor.crosshair.set() }
    override func mouseMoved(with event: NSEvent) { NSCursor.crosshair.set() }
    override func mouseEntered(with event: NSEvent) {
        NSCursor.crosshair.set()
        // Keep the window under the pointer key so Esc reaches it.
        window?.makeKey()
    }

    // MARK: Selection

    /// The dragged rectangle in view coordinates (bottom-left origin).
    private var selection: NSRect? {
        guard let a = anchor, let c = current else { return nil }
        return NSRect(x: min(a.x, c.x), y: min(a.y, c.y), width: abs(a.x - c.x), height: abs(a.y - c.y))
    }

    /// The event location, clamped to this display. The window that received
    /// mouseDown keeps receiving drags even when the pointer leaves its
    /// display, so this confines a selection to the display it started on.
    private func point(of event: NSEvent) -> NSPoint {
        let p = convert(event.locationInWindow, from: nil)
        return NSPoint(x: min(max(p.x, 0), bounds.width), y: min(max(p.y, 0), bounds.height))
    }

    override func mouseDown(with event: NSEvent) {
        anchor = point(of: event)
        current = anchor
        needsDisplay = true
    }

    override func mouseDragged(with event: NSEvent) {
        current = point(of: event)
        needsDisplay = true
    }

    override func mouseUp(with event: NSEvent) {
        current = point(of: event)
        guard let rect = selection, rect.width >= minSize, rect.height >= minSize else {
            anchor = nil
            current = nil
            needsDisplay = true
            return
        }
        onFinish(rect, screen)
    }

    override func rightMouseDown(with event: NSEvent) { onCancel() }

    override func keyDown(with event: NSEvent) {
        if event.keyCode == escKeyCode {
            onCancel()
        } else {
            super.keyDown(with: event)
        }
    }

    // MARK: Drawing

    override func draw(_ dirtyRect: NSRect) {
        NSColor.black.withAlphaComponent(0.35).setFill()
        bounds.fill()

        guard let rect = selection, rect.width > 0, rect.height > 0 else {
            drawHint()
            return
        }

        // Punch a fully transparent hole for the selection.
        NSColor.clear.setFill()
        rect.fill(using: .copy)

        let border = NSBezierPath(rect: rect.insetBy(dx: -0.5, dy: -0.5))
        border.lineWidth = 1
        NSColor.white.setStroke()
        border.stroke()

        drawSizeLabel(for: rect)
    }

    private func drawHint() {
        drawLabel(
            "Drag to select an area  ·  Esc or right-click to cancel",
            font: .systemFont(ofSize: 15, weight: .medium),
            padding: 10,
            at: { size in NSPoint(x: (self.bounds.width - size.width) / 2, y: (self.bounds.height - size.height) / 2) }
        )
    }

    /// Shows the capture size in pixels, below the selection when there is
    /// room, otherwise just inside its bottom edge.
    private func drawSizeLabel(for rect: NSRect) {
        let scale = screen.backingScaleFactor
        let text = "\(Int((rect.width * scale).rounded())) × \(Int((rect.height * scale).rounded()))"
        drawLabel(text, font: .monospacedDigitSystemFont(ofSize: 12, weight: .medium), padding: 4) { size in
            var origin = NSPoint(x: rect.minX, y: rect.minY - size.height - 6)
            if origin.y < 0 { origin.y = rect.minY + 6 }
            origin.x = min(origin.x, self.bounds.width - size.width)
            return origin
        }
    }

    /// Draws text on a dark rounded background. position receives the size of
    /// the background and returns its bottom-left corner.
    private func drawLabel(_ text: String, font: NSFont, padding: CGFloat, at position: (NSSize) -> NSPoint) {
        let attrs: [NSAttributedString.Key: Any] = [.font: font, .foregroundColor: NSColor.white]
        let textSize = (text as NSString).size(withAttributes: attrs)
        let size = NSSize(width: textSize.width + padding * 2, height: textSize.height + padding * 2)
        let background = NSRect(origin: position(size), size: size)

        NSColor.black.withAlphaComponent(0.75).setFill()
        NSBezierPath(roundedRect: background, xRadius: 5, yRadius: 5).fill()
        (text as NSString).draw(at: NSPoint(x: background.minX + padding, y: background.minY + padding), withAttributes: attrs)
    }
}

/// One overlay window per display.
final class Overlay {
    private var windows: [OverlayWindow] = []

    func show() {
        for screen in NSScreen.screens {
            let window = OverlayWindow(contentRect: screen.frame, styleMask: .borderless, backing: .buffered, defer: false)
            window.setFrame(screen.frame, display: false)
            window.level = .screenSaver // above the menu bar and Dock
            window.isOpaque = false
            window.backgroundColor = .clear
            window.hasShadow = false
            window.ignoresMouseEvents = false // catch clicks on transparent pixels too
            window.acceptsMouseMovedEvents = true
            window.isReleasedWhenClosed = false
            window.collectionBehavior = [.canJoinAllSpaces, .fullScreenAuxiliary, .stationary, .ignoresCycle]

            let view = SelectionView(screen: screen, onFinish: finish, onCancel: cancel)
            window.contentView = view
            window.makeKeyAndOrderFront(nil)
            window.makeFirstResponder(view)
            windows.append(window)
        }

        // Start with the display under the pointer as the key window.
        let mouse = NSEvent.mouseLocation
        windows.first { $0.frame.contains(mouse) }?.makeKey()
    }

    private func finish(_ rect: NSRect, on screen: NSScreen) {
        hide()
        guard let screenIndex = activeDisplayIndex(of: screen.displayID) else {
            fail("display \(screen.displayID) is not in the active display list")
        }
        // Flip from AppKit's bottom-left origin to the top-left origin ffmpeg uses.
        let selection = Selection(
            x: Double(rect.minX),
            y: Double(screen.frame.height - rect.maxY),
            w: Double(rect.width),
            h: Double(rect.height),
            scale: Double(screen.backingScaleFactor),
            displayID: screen.displayID,
            screenIndex: screenIndex
        )
        let encoder = JSONEncoder()
        encoder.outputFormatting = .sortedKeys
        guard let json = try? encoder.encode(selection) else {
            fail("could not encode selection")
        }
        FileHandle.standardOutput.write(json + Data("\n".utf8))
        exit(0)
    }

    private func cancel() {
        hide()
        FileHandle.standardError.write(Data("select-region: cancelled\n".utf8))
        exit(1)
    }

    private func hide() {
        windows.forEach { $0.orderOut(nil) }
    }
}

/// Dims a display while recording, leaving the recorded area clear and
/// marking it with a dotted border just outside it.
final class FrameView: NSView {
    /// The recorded area in view coordinates, or nil to dim the whole display.
    private let hole: NSRect?

    static let gap: CGFloat = 2 // keeps the border clear of the captured pixels
    static let dotSize: CGFloat = 2
    static let dotSpacing: CGFloat = 5

    init(frame: NSRect, hole: NSRect?) {
        self.hole = hole
        super.init(frame: frame)
    }

    required init?(coder: NSCoder) { fatalError("init(coder:) is not used") }

    override func draw(_ dirtyRect: NSRect) {
        NSColor.black.withAlphaComponent(0.45).setFill()
        bounds.fill()
        guard let hole else { return }

        // Punch a fully transparent hole, so nothing tints the recording.
        NSColor.clear.setFill()
        hole.fill(using: .copy)

        // Round caps on zero-length dashes draw dots.
        let d = FrameView.dotSize
        let border = NSBezierPath(rect: hole.insetBy(dx: -(FrameView.gap + d / 2), dy: -(FrameView.gap + d / 2)))
        border.lineWidth = d
        border.lineCapStyle = .round
        border.setLineDash([0, FrameView.dotSpacing], count: 2, phase: 0)
        NSColor(white: 0.75, alpha: 0.9).setStroke()
        border.stroke()
    }
}

var frameWindows: [NSWindow] = [] // keeps the windows alive

/// Dims every display except rect, given in points from the top-left corner
/// of the display displayID, and marks rect with a border, until stdin closes.
func showFrame(displayID: UInt32, rect: NSRect) {
    guard NSScreen.screens.contains(where: { $0.displayID == displayID }) else {
        fail("display \(displayID) not found")
    }
    for screen in NSScreen.screens {
        var hole: NSRect?
        if screen.displayID == displayID {
            // Flip to AppKit's bottom-left origin, relative to the display.
            hole = NSRect(x: rect.minX, y: screen.frame.height - rect.maxY, width: rect.width, height: rect.height)
        }
        let window = NSWindow(contentRect: screen.frame, styleMask: .borderless, backing: .buffered, defer: false)
        window.setFrame(screen.frame, display: false)
        window.level = .screenSaver
        window.isOpaque = false
        window.backgroundColor = .clear
        window.hasShadow = false
        window.ignoresMouseEvents = true // clicks go through to whatever is underneath
        window.sharingType = .none // and keep it out of screen captures where macOS allows
        window.collectionBehavior = [.canJoinAllSpaces, .fullScreenAuxiliary, .stationary, .ignoresCycle]
        window.contentView = FrameView(frame: NSRect(origin: .zero, size: screen.frame.size), hole: hole)
        window.orderFrontRegardless()
        frameWindows.append(window)
    }

    // prgif holds stdin open while the frame should show; exit when it
    // closes, including when prgif itself exits or crashes.
    FileHandle.standardInput.readabilityHandler = { handle in
        if handle.availableData.isEmpty { exit(0) }
    }
}

func fail(_ message: String) -> Never {
    FileHandle.standardError.write(Data("select-region: \(message)\n".utf8))
    exit(2)
}

guard !NSScreen.screens.isEmpty else { fail("no displays found") }

let app = NSApplication.shared
app.setActivationPolicy(.accessory) // no Dock icon or menu bar

let args = CommandLine.arguments
if args.count > 1 && args[1] == "--frame" {
    guard args.count == 7, let id = UInt32(args[2]),
          let x = Double(args[3]), let y = Double(args[4]), let w = Double(args[5]), let h = Double(args[6])
    else { fail("usage: select-region --frame DISPLAYID X Y W H") }
    showFrame(displayID: id, rect: NSRect(x: x, y: y, width: w, height: h))
    app.run() // without activating: the terminal keeps focus
}

let overlay = Overlay()
overlay.show()
// Launched from a terminal, the helper has to take focus to receive Esc.
// activate() on macOS 14+ only asks politely and the terminal may keep focus,
// so force it; right-click cancels either way.
app.activate(ignoringOtherApps: true)
app.run()
