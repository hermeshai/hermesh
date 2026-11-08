// Renders one page with PDFKit (the engine behind Preview.app), annotations included.
import AppKit
import PDFKit

let a = CommandLine.arguments
let doc = PDFDocument(url: URL(fileURLWithPath: a[1]))!
if doc.isEncrypted && a.count > 4 { _ = doc.unlock(withPassword: a[4]) }
let page = doc.page(at: Int(a[2])! - 1)!
let b = page.bounds(for: .mediaBox)
let scale: CGFloat = 1.5
let rep = NSBitmapImageRep(bitmapDataPlanes: nil, pixelsWide: Int(b.width * scale), pixelsHigh: Int(b.height * scale), bitsPerSample: 8, samplesPerPixel: 4, hasAlpha: true, isPlanar: false, colorSpaceName: .deviceRGB, bytesPerRow: 0, bitsPerPixel: 0)!
let ctx = NSGraphicsContext(bitmapImageRep: rep)!.cgContext
ctx.setFillColor(NSColor.white.cgColor)
ctx.fill(CGRect(x: 0, y: 0, width: b.width * scale, height: b.height * scale))
ctx.scaleBy(x: scale, y: scale)
page.draw(with: .mediaBox, to: ctx)
try! rep.representation(using: .png, properties: [:])!.write(to: URL(fileURLWithPath: a[3]))
print("annotations:", page.annotations.map { "\($0.type ?? "?")" }.joined(separator: ","))
