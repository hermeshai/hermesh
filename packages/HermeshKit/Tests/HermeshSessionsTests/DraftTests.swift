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

@Test func nextStreamRowEndReleasesFullRowsAndProtectsInlineSyntax() {
    let multi = "## 标题\n\n第一行\n第二行"
    let first = nextStreamRowEnd(multi, from: multi.startIndex, flushTail: false)!
    #expect(String(multi[..<first]) == "## 标题\n")
    let second = nextStreamRowEnd(multi, from: first, flushTail: false)!
    #expect(String(multi[first..<second]) == "\n第一行\n")

    let short = "还没攒够一整排"
    #expect(nextStreamRowEnd(short, from: short.startIndex, flushTail: false) == nil)
    #expect(nextStreamRowEnd(short, from: short.startIndex, flushTail: true) == short.endIndex)

    let prose = "这是一整排在手机宽度下已经超过二十八个汉字的流式输出测试内容，用来确认按整排蓄水切分时不会截断行内代码。"
    let cut = nextStreamRowEnd(prose, from: prose.startIndex, flushTail: false)!
    #expect(prose.distance(from: prose.startIndex, to: cut) >= 22)
    #expect(cut < prose.endIndex)
}

@Test func unwrapCarouselsMergesPureImageSlidesAndSeparatesMixedSlides() {
    let pure = "Before\n\n````carousel\n![Dark](/tmp/dark.png)\n<!-- slide -->\n![Light](/tmp/light.png)\n````\n\nAfter"
    #expect(unwrapCarousels(pure) == "Before\n\n![Dark](/tmp/dark.png)\n![Light](/tmp/light.png)\n\nAfter")

    let mixed = "````carousel\n![Shot](/tmp/a.png)\n<!-- slide -->\n```python\nprint('<!-- slide -->')\n```\n````"
    #expect(unwrapCarousels(mixed) == "![Shot](/tmp/a.png)\n\n---\n\n```python\nprint('<!-- slide -->')\n```")

    let quoted = "`````markdown\n````carousel\n![Literal](/tmp/a.png)\n````\n`````"
    #expect(unwrapCarousels(quoted) == quoted)

    let streaming = "````carousel\n![One](/tmp/1.png)\n<!-- slide -->\n![Two](/tmp/2.png)"
    #expect(unwrapCarousels(streaming) == "![One](/tmp/1.png)\n![Two](/tmp/2.png)")

    let spaced = "See ![shot](/Users/me/Library/Application Support/Hermesh/shot.png) and `[raw](/Application Support/x.png)`"
    #expect(unwrapCarousels(spaced) == "See ![shot](/Users/me/Library/Application%20Support/Hermesh/shot.png) and `[raw](/Application Support/x.png)`")

    let nested = "````carousel\n![方案 A [推荐 1]](/Users/me/Library/Application Support/demo (1).png)\n<!-- slide -->\n![方案 B [推荐 2]](/tmp/demo (2).png)\n````"
    #expect(unwrapCarousels(nested) == "![方案 A [推荐 1]](/Users/me/Library/Application%20Support/demo%20(1).png)\n![方案 B [推荐 2]](/tmp/demo%20(2).png)")
}
