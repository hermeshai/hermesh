import Testing
@testable import HermeshSessions

@Test func textWithoutNotesIsLeftAsItIs() {
    #expect(liftFootnotes("plain [text](x) and ^2").text == "plain [text](x) and ^2")
    #expect(liftFootnotes("a [^1] with no note").text == "a [^1] with no note")
    #expect(liftFootnotes("a [^1] with no note").notes.isEmpty)
}

@Test func referencesAreNumberedInTheOrderTheyAreFirstMade() {
    let lifted = liftFootnotes("""
    First[^b], then[^a], again[^b].

    [^a]: Note A.
    [^b]: Note B.
    """)
    #expect(lifted.text == "First¹, then², again¹.\n")
    #expect(lifted.notes == ["Note B.", "Note A."])
}

@Test func aNoteNothingRefersToIsLeftOut() {
    let lifted = liftFootnotes("Text[^used].\n\n[^used]: Used.\n[^unused]: Unused.")
    #expect(lifted.notes == ["Used."])
    #expect(!lifted.text.contains("Unused"))
}

@Test func aNoteTakesTheLinesIndentedUnderIt() {
    let lifted = liftFootnotes("See[^long].\n\n[^long]: First line.\n    Second line.\n\n    Second paragraph.\nAfter.")
    #expect(lifted.notes == ["First line.\nSecond line.\n\nSecond paragraph."])
    #expect(lifted.text == "See¹.\n\nAfter.")
}

@Test func labelsAreMatchedWithoutCaseAndNumbersPastNineAreSuperscript() {
    let refs = (1 ... 11).map { "[^N\($0)]" }.joined(separator: " ")
    let defs = (1 ... 11).map { "[^n\($0)]: \($0)" }.joined(separator: "\n")
    let lifted = liftFootnotes(refs + "\n\n" + defs)
    #expect(lifted.text.hasPrefix("¹ ² ³ ⁴ ⁵ ⁶ ⁷ ⁸ ⁹ ¹⁰ ¹¹"))
    #expect(lifted.notes.count == 11)
}

@Test func codeKeepsWhatWasWritten() {
    let lifted = liftFootnotes("""
    Real[^1] and `code [^1]` and ``two `[^1]` ticks``.

    ```
    [^1] in a block
    [^1]: not a note
    ```

    [^1]: The note.
    """)
    #expect(lifted.text.hasPrefix("Real¹ and `code [^1]` and ``two `[^1]` ticks``."))
    #expect(lifted.text.contains("[^1] in a block\n[^1]: not a note"))
    #expect(lifted.notes == ["The note."])
}
