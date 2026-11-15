import HermeshHer
import HermeshKit
import SwiftUI

/// What a page shows for something the machine's Hermesh doesn't have yet: that machine is older than
/// this app (docs/version-skew.md §6.2). The page loads by itself once the machine says it has it.
struct NeedsUpdateView: View {
    let machineName: String

    var body: some View {
        ContentUnavailableView {
            Label("Hermesh Needs an Update", systemImage: "arrow.down.circle")
        } description: {
            Text("Hermesh on \(machineName) doesn’t have this yet. Update Hermesh there, and this page loads by itself.")
        }
        .accessibilityIdentifier("needs-update")
    }
}

/// The error says the machine is older than this app: her lacks the method, or hermeshd or its
/// services don't know the call (docs/version-skew.md §6.3).
func needsUpdate(_ error: Error) -> Bool {
    if let e = error as? HerRPCError { return e.needsUpdate }
    if let e = error as? APIError { return e.code == "unimplemented" }
    return false
}
