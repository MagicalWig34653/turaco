// Native macOS helper: SVG rasterization and font outlines; no package dependencies.
import AppKit
import CoreText

let root = URL(fileURLWithPath: CommandLine.arguments[1], isDirectory: true)
let source = root.appendingPathComponent("source", isDirectory: true)
let output = root.appendingPathComponent("export", isDirectory: true)
func write(_ text: String, _ url: URL) throws { try (text + "\n").write(to: url, atomically: true, encoding: .utf8) }
func inner(_ name: String) throws -> String {
    let svg = try String(contentsOf: source.appendingPathComponent(name + ".svg"), encoding: .utf8)
    return String(svg[svg.index(after: svg.firstIndex(of: ">")!)..<svg.range(of: "</svg>")!.lowerBound])
}
func svg(_ body: String, width: Int = 1024, height: Int = 1024) -> String {
    "<svg xmlns=\"http://www.w3.org/2000/svg\" width=\"\(width)\" height=\"\(height)\" viewBox=\"0 0 \(width) \(height)\">\n\(body)\n</svg>"
}
func raster(_ url: URL, to target: URL, width: Int, height: Int) throws {
    guard let image = NSImage(contentsOf: url),
          let bitmap = NSBitmapImageRep(bitmapDataPlanes: nil, pixelsWide: width, pixelsHigh: height,
            bitsPerSample: 8, samplesPerPixel: 4, hasAlpha: true, isPlanar: false,
            colorSpaceName: .deviceRGB, bytesPerRow: 0, bitsPerPixel: 0),
          let context = NSGraphicsContext(bitmapImageRep: bitmap) else { fatalError("Cannot load \(url.path)") }
    NSGraphicsContext.saveGraphicsState()
    NSGraphicsContext.current = context
    context.imageInterpolation = .high
    image.draw(in: NSRect(x: 0, y: 0, width: width, height: height), from: .zero, operation: .copy, fraction: 1)
    context.flushGraphics()
    NSGraphicsContext.restoreGraphicsState()
    guard let png = bitmap.representation(using: .png, properties: [:]) else { fatalError("PNG encoding failed") }
    try png.write(to: target)
}
func number(_ v: CGFloat) -> String { String(format: "%.2f", Double(v)) }
func textPaths(_ text: String, size: CGFloat, bold: Bool, x: CGFloat, y: CGFloat) -> String {
    let font = NSFont.systemFont(ofSize: size, weight: bold ? .semibold : .regular)
    let line = CTLineCreateWithAttributedString(NSAttributedString(string: text, attributes: [.font: font]))
    var paths = ""
    for run in CTLineGetGlyphRuns(line) as! [CTRun] {
        let count = CTRunGetGlyphCount(run)
        var glyphs = [CGGlyph](repeating: 0, count: count)
        var positions = [CGPoint](repeating: .zero, count: count)
        CTRunGetGlyphs(run, CFRange(location: 0, length: 0), &glyphs)
        CTRunGetPositions(run, CFRange(location: 0, length: 0), &positions)
        let runFont = (CTRunGetAttributes(run) as NSDictionary)[kCTFontAttributeName] as! CTFont
        for i in 0..<count {
            guard let path = CTFontCreatePathForGlyph(runFont, glyphs[i], nil) else { continue }
            var d = ""
            func point(_ p: CGPoint) -> String { "\(number(x + positions[i].x + p.x)) \(number(y - p.y))" }
            path.applyWithBlock { element in
                let e = element.pointee
                switch e.type {
                case .moveToPoint: d += "M" + point(e.points[0])
                case .addLineToPoint: d += "L" + point(e.points[0])
                case .addQuadCurveToPoint: d += "Q" + point(e.points[0]) + " " + point(e.points[1])
                case .addCurveToPoint: d += "C" + point(e.points[0]) + " " + point(e.points[1]) + " " + point(e.points[2])
                case .closeSubpath: d += "Z"
                @unknown default: break
                }
            }
            paths += "<path d=\"\(d)\"/>\n"
        }
    }
    return paths
}
let mark = try ["crest", "head", "feather"].map { try inner($0) }.joined(separator: "\n")
let background = """
<defs><linearGradient id="bg" x1="0" y1="0" x2="1" y2="1"><stop stop-color="#327858"/><stop offset="1" stop-color="#142e25"/></linearGradient></defs>
<path fill="url(#bg)" d="M0 0H1024V1024H0Z"/>
"""
// Full-bleed web assets: platforms supply their own masks. Native glass stays in .icon.
try write(svg(background + mark), source.appendingPathComponent("web-icon.svg"))
// Every foreground point is inside radius 512 * .8 / 2 after scaling (40% safe circle).
try write(svg(background + "<g transform=\"translate(102.4 102.4) scale(.8)\">" + mark + "</g>"), source.appendingPathComponent("maskable-icon.svg"))
let silhouette = "M308 760C304 676 334 612 391 551C416 524 421 500 419 465L348 490C294 420 260 332 264 252C337 265 396 307 435 363C406 288 408 218 436 172C497 214 532 283 536 351C557 287 598 245 651 225C663 271 660 315 645 353C669 371 687 393 695 417L782 468Q798 480 782 489L678 516C655 545 626 560 589 568C572 652 603 715 655 776C533 807 401 801 308 760ZM642 439A22 22 0 1 0 598 439A22 22 0 1 0 642 439Z"
try write(svg("<style>path{fill:#246747}@media(prefers-color-scheme:dark){path{fill:#edf3df}}</style><path fill-rule=\"evenodd\" d=\"\(silhouette)\"/>"), output.appendingPathComponent("favicon.svg"))
try write(svg("<path fill=\"#000\" fill-rule=\"evenodd\" d=\"\(silhouette)\"/>"), output.appendingPathComponent("mask-icon.svg"))
let socialBackground = background.replacingOccurrences(of: "<path fill=\"url(#bg)\" d=\"M0 0H1024V1024H0Z\"/>", with: "<rect width=\"1024\" height=\"1024\" rx=\"226\" fill=\"url(#bg)\"/>")
let social = """
<path fill="#f5f3eb" d="M0 0H1200V630H0Z"/>
<g transform="translate(78 155) scale(.3125)" >\(socialBackground)\(mark)</g>
<g fill="#142e25">\(textPaths("Turaco", size: 98, bold: true, x: 454, y: 312))</g>
<g fill="#246747">\(textPaths("One connected workspace", size: 29, bold: false, x: 460, y: 374))\(textPaths("for IT operations", size: 29, bold: false, x: 460, y: 414))</g>
"""
try write(svg(social, width: 1200, height: 630), source.appendingPathComponent("og-image.svg"))
try raster(source.appendingPathComponent("web-icon.svg"), to: output.appendingPathComponent("icon-512.png"), width: 512, height: 512)
try raster(source.appendingPathComponent("maskable-icon.svg"), to: output.appendingPathComponent("icon-maskable-512.png"), width: 512, height: 512)
try raster(source.appendingPathComponent("og-image.svg"), to: output.appendingPathComponent("og-image.png"), width: 1200, height: 630)
