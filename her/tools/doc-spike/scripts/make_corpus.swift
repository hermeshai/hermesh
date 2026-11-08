// Generates test PDFs: a Chinese report (CoreText, embedded PingFang) and an AcroForm (PDFKit).
import AppKit
import PDFKit

let out = CommandLine.arguments[1]

func report(_ path: String, pages: Int) {
    var box = CGRect(x: 0, y: 0, width: 595, height: 842)
    let ctx = CGContext(URL(fileURLWithPath: path) as CFURL, mediaBox: &box, nil)!
    let font = NSFont(name: "PingFangSC-Regular", size: 11)!
    let bold = NSFont(name: "PingFangSC-Semibold", size: 16)!
    func draw(_ s: String, _ f: NSFont, _ rect: CGRect) {
        let a = NSAttributedString(string: s, attributes: [.font: f, .foregroundColor: NSColor.black])
        let fs = CTFramesetterCreateWithAttributedString(a)
        let frame = CTFramesetterCreateFrame(fs, CFRange(location: 0, length: 0), CGPath(rect: rect, transform: nil), nil)
        CTFrameDraw(frame, ctx)
    }
    let para = "本公司2025年度实现营业收入1,234,567.89万元，同比增长12.5%；归属于上市公司股东的净利润98,765.43万元，同比下降3.2%。The Company's revenue grew 12.5% year over year, driven by fiscal-year efficiencies in office automation. 违约金为合同总额的20%，应于收到通知后十五个工作日内支付。"
    for p in 0..<pages {
        ctx.beginPDFPage(nil)
        draw("第\(p + 1)页 · 2025年年度报告摘要", bold, CGRect(x: 50, y: 770, width: 495, height: 30))
        if p % 2 == 0 {
            draw(String(repeating: para + "\n\n", count: 4), font, CGRect(x: 50, y: 420, width: 495, height: 340))
        } else {  // two columns
            draw(String(repeating: para + "\n", count: 3), font, CGRect(x: 50, y: 420, width: 235, height: 340))
            draw(String(repeating: para + "\n", count: 3), font, CGRect(x: 310, y: 420, width: 235, height: 340))
        }
        // a simple table: label + right-aligned numbers
        let rows = [("项目", "2025年", "2024年"), ("营业收入", "1,234,567.89", "1,097,393.68"), ("营业成本", "(876,543.21)", "(790,123.45)"), ("净利润", "98,765.43", "102,031.64")]
        for (i, r) in rows.enumerated() {
            let y = CGFloat(360 - i * 22)
            draw(r.0, font, CGRect(x: 50, y: y, width: 200, height: 18))
            draw(r.1, font, CGRect(x: 300, y: y, width: 110, height: 18))
            draw(r.2, font, CGRect(x: 430, y: y, width: 110, height: 18))
        }
        ctx.endPDFPage()
    }
    ctx.closePDF()
}

func form(_ path: String) {
    let doc = PDFDocument()
    let page = PDFPage()
    page.setBounds(CGRect(x: 0, y: 0, width: 595, height: 842), for: .mediaBox)
    doc.insert(page, at: 0)
    for (i, name) in ["company", "signer", "date"].enumerated() {
        let w = PDFAnnotation(bounds: CGRect(x: 150, y: 700 - i * 40, width: 250, height: 24), forType: .widget, withProperties: nil)
        w.widgetFieldType = .text
        w.fieldName = name
        w.font = NSFont.systemFont(ofSize: 12)
        page.addAnnotation(w)
        let label = PDFAnnotation(bounds: CGRect(x: 50, y: 700 - i * 40, width: 90, height: 24), forType: .freeText, withProperties: nil)
        label.contents = name
        label.font = NSFont.systemFont(ofSize: 12)
        label.color = .clear
        page.addAnnotation(label)
    }
    let cb = PDFAnnotation(bounds: CGRect(x: 150, y: 560, width: 18, height: 18), forType: .widget, withProperties: nil)
    cb.widgetFieldType = .button
    cb.widgetControlType = .checkBoxControl
    cb.fieldName = "agree"
    page.addAnnotation(cb)
    doc.write(toFile: path)
}

report("\(out)/cn_report.pdf", pages: 3)
report("\(out)/cn_report_1000.pdf", pages: 1000)
// form.pdf comes from make_form.py (PDFKit writes widgets without an AcroForm)
print("ok")
