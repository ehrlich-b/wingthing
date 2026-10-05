import Testing

func expectEqual<Value: Equatable>(_ actual: Value, _ expected: Value, sourceLocation: SourceLocation = #_sourceLocation) {
    #expect(actual == expected, sourceLocation: sourceLocation)
}
func expectTrue(_ value: Bool, sourceLocation: SourceLocation = #_sourceLocation) { #expect(value, sourceLocation: sourceLocation) }
func expectFalse(_ value: Bool, sourceLocation: SourceLocation = #_sourceLocation) { #expect(!value, sourceLocation: sourceLocation) }
func expectNil<Value>(_ value: Value?, sourceLocation: SourceLocation = #_sourceLocation) { #expect(value == nil, sourceLocation: sourceLocation) }
func unwrap<Value>(_ value: Value?, sourceLocation: SourceLocation = #_sourceLocation) throws -> Value { try #require(value, sourceLocation: sourceLocation) }
func fail(_ message: String, sourceLocation: SourceLocation = #_sourceLocation) { Issue.record(Comment(rawValue: message), sourceLocation: sourceLocation) }
func expectThrows<Value>(_ action: @autoclosure () throws -> Value, sourceLocation: SourceLocation = #_sourceLocation) {
    do { _ = try action(); Issue.record("Expected an error", sourceLocation: sourceLocation) } catch {}
}
func expectNoThrow<Value>(_ action: @autoclosure () throws -> Value, sourceLocation: SourceLocation = #_sourceLocation) {
    do { _ = try action() } catch { Issue.record(error, sourceLocation: sourceLocation) }
}
