import HermeshKit
import SwiftUI

/// Onboarding until this iPhone belongs to an owner, then the app.
struct RootView: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        @Bindable var model = model
        Group {
            switch model.phase {
            case .starting:
                Mark(size: 72).frame(maxWidth: .infinity, maxHeight: .infinity)
            case .failed(let message):
                ContentUnavailableView("Hermesh couldn’t start", systemImage: "exclamationmark.triangle", description: Text(message))
            case .onboarding:
                OnboardingView()
            case .ready:
                MainView()
            }
        }
        // Signing: the confirmation for a batch the app had not asked for,
        // and the digits to type on a device just approved.
        .sheet(item: Binding(get: { model.signer.shown }, set: { if $0 == nil { model.signer.rejectShown() } })) { c in
            SigningSheet(confirmation: c).environment(model)
        }
        .sheet(isPresented: Binding(get: { model.signer.match != nil }, set: { if !$0 { model.signer.match = nil } })) {
            if let m = model.signer.match { MatchSheet(label: m.label, digits: m.digits) }
        }
        .alert("Something went wrong", isPresented: Binding(get: { model.problem != nil || model.signer.failure != nil }, set: { if !$0 { model.problem = nil; model.signer.failure = nil } })) {
            Button("OK", role: .cancel) {}
        } message: {
            Text(model.problem ?? model.signer.failure ?? "")
        }
    }
}
