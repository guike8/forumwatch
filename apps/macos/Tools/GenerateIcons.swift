import AppKit
import Foundation

@main
struct IconGenerator {
    static func main() throws {
        guard CommandLine.arguments.count == 2 else { fatalError("Pass an output directory") }
        let output = URL(fileURLWithPath: CommandLine.arguments[1], isDirectory: true)
        let iconset = output.appendingPathComponent("Nightwire.iconset", isDirectory: true)
        try FileManager.default.createDirectory(at: iconset, withIntermediateDirectories: true)
        for points in [16, 32, 128, 256, 512] {
            for scale in [1, 2] {
                let suffix = scale == 1 ? "" : "@2x"
                try render(pixels: points * scale).write(to: iconset.appendingPathComponent("icon_\(points)x\(points)\(suffix).png"))
            }
        }
        try render(pixels: 1024).write(to: output.appendingPathComponent("Nightwire-preview.png"))
    }

    private static func render(pixels: Int) throws -> Data {
        guard let bitmap = NSBitmapImageRep(bitmapDataPlanes: nil, pixelsWide: pixels, pixelsHigh: pixels,
                                            bitsPerSample: 8, samplesPerPixel: 4, hasAlpha: true,
                                            isPlanar: false, colorSpaceName: .deviceRGB, bytesPerRow: 0, bitsPerPixel: 0),
              let context = NSGraphicsContext(bitmapImageRep: bitmap) else { fatalError("Cannot draw icon") }
        NSGraphicsContext.saveGraphicsState()
        defer { NSGraphicsContext.restoreGraphicsState() }
        NSGraphicsContext.current = context
        context.cgContext.scaleBy(x: CGFloat(pixels) / 1024, y: CGFloat(pixels) / 1024)
        context.cgContext.setShouldAntialias(true)

        let tile = NSBezierPath(roundedRect: NSRect(x: 64, y: 64, width: 896, height: 896), xRadius: 196, yRadius: 196)
        NSColor(srgbRed: 36 / 255, green: 35 / 255, blue: 42 / 255, alpha: 1).setFill()
        tile.fill()
        let chalk = NSColor(srgbRed: 231 / 255, green: 224 / 255, blue: 210 / 255, alpha: 1)
        MothArtwork.draw(in: NSRect(x: 24, y: 24, width: 976, height: 976), tint: chalk, simplified: pixels <= 64)

        guard let data = bitmap.representation(using: .png, properties: [:]) else { fatalError("Cannot encode icon") }
        return data
    }
}
