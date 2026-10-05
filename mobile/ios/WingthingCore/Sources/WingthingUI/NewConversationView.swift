import SwiftUI
import WingthingCore

struct NewConversationView: View {
    @ObservedObject var model: WingthingModel
    @Environment(\.dismiss) private var dismiss
    @State private var label = "personal-parent"
    @State private var workspace = ""
    @State private var providerModel = "claude-opus-5-5"
    @State private var input = ""
    private var saved: PendingConversationLaunch? { model.pendingLaunch }
    private var canSubmit: Bool {
        !model.busy && model.connected && (saved.map { !$0.progress.final } ??
            (model.canCreateConversation && !label.isEmpty && !workspace.isEmpty && !providerModel.isEmpty && !input.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty))
    }
    var body: some View {
        Form {
            Section {
                Button("Done") { dismiss() }.font(.body).frame(minHeight: 44)
                    .accessibilityIdentifier("close-new-conversation")
            }
            Section("On your computer") {
                Text("Runs in an existing project on your computer.")
                    .fixedSize(horizontal: false, vertical: true)
                if let saved {
                    LabeledContent("Project", value: saved.workspace)
                    LabeledContent("Name", value: saved.label)
                    LabeledContent("Model", value: saved.model)
                } else {
                    if let options = model.creationOptions, !options.projects.isEmpty {
                        ForEach(options.projects) { project in
                            Button { workspace = project.path } label: {
                                HStack(alignment: .top, spacing: 12) {
                                    Text(project.name).fixedSize(horizontal: false, vertical: true)
                                        .frame(maxWidth: .infinity, alignment: .leading)
                                    Image(systemName: workspace == project.path ? "checkmark.circle.fill" : "circle")
                                        .accessibilityHidden(true)
                                }.foregroundStyle(.primary).frame(minHeight: 44)
                            }.accessibilityLabel("Project: " + project.name)
                                .accessibilityValue(workspace == project.path ? "Selected" : "Not selected")
                                .accessibilityIdentifier("new-conversation-project")
                        }
                        if !workspace.isEmpty { Text(workspace).font(.footnote).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true) }
                    }
                    TextField("Conversation name", text: $label).plainEntry()
                        .accessibilityIdentifier("new-conversation-name")
                    TextField("Exact Claude model", text: $providerModel).plainEntry()
                        .accessibilityIdentifier("new-conversation-model")
                }
                Text("Uses the Claude login already configured on that computer. A model request begins only when you tap Create conversation.")
                    .font(.footnote).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            }
            Section("First message") {
                if let saved {
                    Text(saved.input).textSelection(.enabled).accessibilityIdentifier("saved-first-message")
                        .fixedSize(horizontal: false, vertical: true)
                } else {
                    TextEditor(text: $input).frame(minHeight: 130)
                        .accessibilityLabel("First message").accessibilityIdentifier("new-conversation-input")
                }
            }
            Section {
                if let notice = model.creationNotice {
                    Text(notice).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
                        .accessibilityIdentifier("new-conversation-notice")
                }
                if let saved, !saved.progress.final {
                    Text("Creation is unconfirmed. This message and project are saved on this phone. Retry uses the same launch and cannot create it twice.")
                        .fixedSize(horizontal: false, vertical: true).accessibilityIdentifier("saved-conversation-launch")
                }
                if saved?.progress == .failed {
                    Button("New attempt after confirmed failure") { model.newConversationAttempt() }
                        .disabled(model.busy)
                } else {
                    Button(saved == nil ? "Create conversation" : "Retry creation") {
                        let prior = model.selected
                        Task {
                            await model.createConversation(label: label, workspace: workspace, model: providerModel, input: input)
                            if model.pendingLaunch == nil, model.selected != prior { dismiss() }
                        }
                    }.disabled(!canSubmit).accessibilityIdentifier("create-conversation")
                }
                if !model.connected {
                    Text(model.phase.title).foregroundStyle(.secondary)
                    Button("Reconnect to your home") {
                        Task { await model.refresh(); await model.prepareConversationCreation() }
                    }.disabled(!model.canReconnect)
                }
            }
        }.scrollDismissesKeyboard(.interactively).navigationTitle("New conversation").inlineConversationTitle()
            .task {
                await model.prepareConversationCreation()
                if let saved {
                    label = saved.label; workspace = saved.workspace; providerModel = saved.model; input = saved.input
                } else if workspace.isEmpty { workspace = model.creationOptions?.projects.first?.path ?? "" }
            }
    }
}
