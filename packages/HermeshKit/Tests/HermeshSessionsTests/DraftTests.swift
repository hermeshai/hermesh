import Testing
@testable import HermeshSessions

@Test func splitBlocksCutsAtBlankLinesBeforeUnindentedText() {
    #expect(splitBlocks("## Title\n\nOne\ntwo\n\n- a\n- b") == ["## Title\n", "One\ntwo\n", "- a\n- b"])
    #expect(splitBlocks("- item\n\n  more of it\n\nNext") == ["- item\n\n  more of it\n", "Next"])
}

@Test func splitBlocksNeverCutsInsideACodeFence() {
    #expect(splitBlocks("```go\na\n\nb\n```\n\nAfter") == ["```go\na\n\nb\n```\n", "After"])
    #expect(splitBlocks("Text\n\n~~~\na\n\nb") == ["Text\n", "~~~\na\n\nb"])
}

@Test func splitBlocksPutBackTogetherAsTheyWere() {
    let text = "A\n\n| a | b |\n|---|---|\n| 1 | 2 |\n\n```\nx\n\ny\n```\n\n1. one\n\n2. two"
    #expect(splitBlocks(text).joined(separator: "\n") == text)
}

@Test func withoutOpenTableHoldsATableBackUntilItsDelimiterRow() {
    #expect(withoutOpenTable("Results:\n| a | b |") == "Results:")
    #expect(withoutOpenTable("| a | b |\n|---|") == "")
    #expect(withoutOpenTable("| a | b |\n|---|:--:|") == "| a | b |\n|---|:--:|")
    #expect(withoutOpenTable("| a | b |\n|---|---|\n| 1 |") == "| a | b |\n|---|---|\n| 1 |")
    #expect(withoutOpenTable("No table | here") == "No table | here")
}

@Test func closingOpenMarksClosesWhatIsStillOpen() {
    #expect(closingOpenMarks("Some **bold") == "Some **bold**")
    #expect(closingOpenMarks("Run `go te") == "Run `go te`")
    #expect(closingOpenMarks("an *aside") == "an *aside*")
    #expect(closingOpenMarks("**bold *both") == "**bold *both***")
    #expect(closingOpenMarks("~~gone") == "~~gone~~")
    #expect(closingOpenMarks("See [the docs](https://exa") == "See the docs")
    #expect(closingOpenMarks("See [the do") == "See the do")
}

@Test func closingOpenMarksLeavesFinishedTextAlone() {
    #expect(closingOpenMarks("* one\n* two **done**") == "* one\n* two **done**")
    #expect(closingOpenMarks("`a*b` and \\*") == "`a*b` and \\*")
    #expect(closingOpenMarks("See [the docs](https://example.com) now") == "See [the docs](https://example.com) now")
    #expect(closingOpenMarks("```go\nfmt.Println(\"**\")") == "```go\nfmt.Println(\"**\")")
}
