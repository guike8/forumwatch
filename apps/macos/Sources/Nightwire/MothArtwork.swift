import AppKit

// Shared original line artwork for the app icon and monochrome menu-bar template.
enum MothArtwork {
    static func draw(in rect: NSRect, tint: NSColor, simplified: Bool) {
        NSGraphicsContext.saveGraphicsState()
        defer { NSGraphicsContext.restoreGraphicsState() }
        guard let context = NSGraphicsContext.current?.cgContext else { return }
        context.translateBy(x: rect.minX, y: rect.maxY)
        context.scaleBy(x: rect.width / 512, y: -rect.height / 512)
        let outline: CGFloat = simplified ? 12 : 3.4
        let vein: CGFloat = simplified ? 9 : 1.7
        for mirrored in [false, true] {
            NSGraphicsContext.saveGraphicsState()
            if mirrored {
                context.translateBy(x: 512, y: 0)
                context.scaleBy(x: -1, y: 1)
            }
            stroke("M 240 218 C 201 162 133 96 44 96 C 42 126 54 161 63 180 C 57 195 73 224 91 238 C 89 258 113 282 139 291 C 166 301 216 275 240 242", tint, outline)
            stroke("M 166 291 C 145 302 125 316 118 329 C 113 356 127 390 147 400 L 158 388 C 170 414 191 427 209 417 L 219 400 C 236 402 246 374 248 350", tint, outline)
            stroke("M 246 199 C 238 167 220 140 208 126", tint, outline * 0.8)
            if simplified {
                stroke("M 104 150 Q 141 202 197 191", tint, vein)
                stroke("M 164 333 C 164 307 203 302 203 325 C 203 351 164 359 164 333 Z", tint, vein)
            } else {
                stroke("M 237 226 C 190 184 128 136 60 111", tint, vein)
                stroke("M 235 234 C 168 227 111 190 66 139", tint, vein)
                stroke("M 231 244 C 177 258 118 234 78 203", tint, vein)
                stroke("M 225 259 C 195 290 161 298 145 294", tint, vein)
                stroke("M 235 286 C 217 296 207 297 199 296 M 160 319 C 150 340 145 364 147 382", tint, vein)
                stroke("M 241 306 C 227 347 209 378 180 405", tint, vein)
                stroke("M 246 330 Q 239 369 218 394", tint, vein)
                stroke("M 55 113 C 62 143 72 165 79 179 C 77 195 89 217 104 228 C 105 246 124 265 146 274 C 159 285 170 285 178 282", tint, 1.1)
                stroke("M 130 334 C 128 356 137 376 147 382 M 164 374 C 176 400 193 410 205 402", tint, 1.1)
                stroke("M 111 150 C 121 178 139 199 162 203 C 180 207 194 200 200 188 C 175 194 146 184 111 150 Z", tint, 2.2)
                stroke("M 158 339 C 160 321 175 305 190 303 C 204 301 215 314 208 330 C 202 346 184 359 171 354 C 164 352 159 346 158 339 Z", tint, 2.2)
                stroke("M 174 336 C 171 326 179 316 187 317 C 198 320 194 334 186 339 C 180 343 176 341 174 336 Z", tint, 1.5)
                stroke("M 148 365 Q 174 385 209 363", tint, 2)
                for index in 0..<6 {
                    let x = 213 + index * 5, y = 135 + index * 8
                    stroke("M \(x) \(y) L \(x - 12) \(y - 2) M \(x) \(y) L \(x + 2) \(y - 11)", tint, 1.6)
                }
                for index in 0..<6 {
                    let x = 79 + index * 15, y = 165 + index * 15
                    stroke("M \(x) \(y) L \(x + 4) \(y + 8)", tint, 1.1)
                }
            }
            NSGraphicsContext.restoreGraphicsState()
        }
        stroke("M 256 195 C 241 195 235 213 243 232 C 235 242 236 258 243 269 C 236 305 242 351 256 378 C 270 351 276 305 269 269 C 276 258 277 242 269 232 C 277 213 271 195 256 195 Z", tint, outline)
        stroke("M 244 231 Q 256 237 268 231 M 244 269 Q 256 276 268 269", tint, vein)
        if !simplified {
            for index in 0..<6 {
                let y = 282 + index * 13
                stroke("M 246 \(y) Q 256 \(y + 8) 266 \(y)", tint, 1.4)
            }
            stroke("M 251 244 L 248 253 M 256 243 L 256 257 M 261 244 L 264 253", tint, 1.2)
        }
    }

    private static func stroke(_ source: String, _ tint: NSColor, _ width: CGFloat) {
        let path = path(source)
        path.lineWidth = width
        path.lineCapStyle = .round
        path.lineJoinStyle = .round
        tint.setStroke()
        path.stroke()
    }

    // Only trusted, literal artwork above is parsed; no source/forum text is used.
    private static let tokens = try! NSRegularExpression(pattern: "[MLCQZ]|-?[0-9]+(?:\\.[0-9]+)?")
    private static func path(_ source: String) -> NSBezierPath {
        let text = source as NSString
        let values = tokens.matches(in: source, range: NSRange(location: 0, length: text.length)).map { text.substring(with: $0.range) }
        let path = NSBezierPath()
        var index = 0
        func number() -> CGFloat { defer { index += 1 }; return CGFloat(Double(values[index])!) }
        func point() -> NSPoint { let x = number(); let y = number(); return NSPoint(x: x, y: y) }
        while index < values.count {
            let command = values[index]; index += 1
            switch command {
            case "M": path.move(to: point())
            case "L": path.line(to: point())
            case "C":
                let first = point(), second = point(), end = point()
                path.curve(to: end, controlPoint1: first, controlPoint2: second)
            case "Q":
                let start = path.currentPoint, control = point(), end = point()
                path.curve(to: end,
                           controlPoint1: NSPoint(x: start.x + (control.x - start.x) * 2 / 3, y: start.y + (control.y - start.y) * 2 / 3),
                           controlPoint2: NSPoint(x: end.x + (control.x - end.x) * 2 / 3, y: end.y + (control.y - end.y) * 2 / 3))
            case "Z": path.close()
            default: preconditionFailure("Invalid artwork command")
            }
        }
        return path
    }
}
