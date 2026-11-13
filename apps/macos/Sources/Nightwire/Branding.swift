import AppKit

@MainActor
enum Branding {
    // The outline-only moth is a template so macOS supplies light/dark contrast.
    static let menuBarIcon: NSImage = {
        let image = NSImage(size: NSSize(width: 18, height: 18), flipped: false) { rect in
            MothArtwork.draw(in: rect, tint: .black, simplified: true)
            return true
        }
        image.isTemplate = true
        return image
    }()
}
