// Crops the top part of several PNGs and stacks them side by side with labels: montage out.png h img1 img2 ...
import AppKit
let a = CommandLine.arguments
let h = CGFloat(Double(a[2])!)
let imgs = a[3...].map { NSImage(contentsOfFile: $0)! }
let w: CGFloat = 600
let out = NSImage(size: NSSize(width: w * CGFloat(imgs.count), height: h + 24))
out.lockFocus()
NSColor.white.setFill(); NSRect(x: 0, y: 0, width: out.size.width, height: out.size.height).fill()
for (i, img) in imgs.enumerated() {
    let s = w / img.size.width
    let src = NSRect(x: 0, y: img.size.height - h / s, width: img.size.width, height: h / s)
    img.draw(in: NSRect(x: CGFloat(i) * w, y: 0, width: w, height: h), from: src, operation: .copy, fraction: 1)
    (a[3 + i] as NSString).lastPathComponent.draw(at: NSPoint(x: CGFloat(i) * w + 6, y: h + 4), withAttributes: [.font: NSFont.systemFont(ofSize: 13)])
}
out.unlockFocus()
let rep = NSBitmapImageRep(data: out.tiffRepresentation!)!
try! rep.representation(using: .png, properties: [:])!.write(to: URL(fileURLWithPath: a[1]))
