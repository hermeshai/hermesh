import Testing
@testable import HermeshSessions

private func spans(_ text: String) -> [MathSpan] { liftMath(text).spans }

@Test func textWithoutMathIsLeftAsItIs() {
    #expect(liftMath("plain text").text == "plain text")
    #expect(spans("It costs $5 and $10.").isEmpty)
    #expect(spans("A path C:\\(x86) and f(x) [1].").isEmpty)
}

@Test func roundParenthesesAreMathInTheLine() {
    let lifted = liftMath("Energy is \\(E = mc^2\\), as he said.")
    #expect(lifted.spans == [MathSpan(tex: "E = mc^2", display: false, raw: "\\(E = mc^2\\)")])
    #expect(mathPieces(lifted.text) == [.text("Energy is "), .math(0), .text(", as he said.")])
    // Not across lines, as on the Mac.
    #expect(spans("a \\(x\nb\\) c").isEmpty)
}

@Test func squareBracketsAreSetApartAndMayTakeLines() {
    let lifted = liftMath("So:\n\\[\n\\frac{a}{b}\n\\]\nDone.")
    #expect(lifted.spans.map(\.tex) == ["\\frac{a}{b}"])
    #expect(lifted.spans[0].display)
    #expect(mathPieces(lifted.text) == [.text("So:\n"), .math(0), .text("\nDone.")])
    // Not across a blank line: that ends the paragraph.
    #expect(spans("\\[ a\n\nb \\]").isEmpty)
}

@Test func dollarsAreMathInALineOrABlock() {
    #expect(spans("The sum $$a_1 + a_2$$ holds.") == [MathSpan(tex: "a_1 + a_2", display: false, raw: "$$a_1 + a_2$$")])
    let block = liftMath("Then:\n\n$$\nx = \\frac{-b \\pm \\sqrt{b^2 - 4ac}}{2a}\n$$\n\nAfter.")
    #expect(block.spans.map(\.tex) == ["x = \\frac{-b \\pm \\sqrt{b^2 - 4ac}}{2a}"])
    #expect(block.spans[0].display)
    #expect(mathPieces(block.text) == [.text("Then:\n\n"), .math(0), .text("\n\nAfter.")])
    // A block still being written has no last line yet.
    #expect(spans("$$\nx = 1").isEmpty)
    #expect(spans("One dollar sign: $x$.").isEmpty)
}

@Test func codeKeepsItsText() {
    #expect(spans("Write `\\(x\\)` for math.").isEmpty)
    #expect(spans("```\n\\(x\\) and $$y$$\n```").isEmpty)
    #expect(spans("~~~tex\n\\[ x \\]\n~~~\n").isEmpty)
    // After the fence closes, math is read again.
    #expect(spans("```\ncode\n```\n\\(x\\)").map(\.tex) == ["x"])
    // A backtick with no partner is only a backtick.
    #expect(spans("a ` b \\(x\\)").map(\.tex) == ["x"])
}

@Test func anEscapedBackslashStartsNoMath() {
    #expect(spans("a line break \\\\(not math\\\\)").isEmpty)
    #expect(spans("\\(a \\\\ b\\)").map(\.tex) == ["a \\\\ b"])
}

@Test func longOrEmptyMathStaysText() {
    #expect(spans("\\(" + String(repeating: "x", count: maxMathLength + 1) + "\\)").isEmpty)
    #expect(spans("\\(\\) and $$$$").isEmpty)
}

@Test func marksGoBackToTheSource() {
    let text = "a \\(x_1\\) b $$y$$ c"
    let lifted = liftMath(text)
    #expect(lifted.text != text)
    #expect(loweringMath(lifted.text, lifted.spans) == text)
}
