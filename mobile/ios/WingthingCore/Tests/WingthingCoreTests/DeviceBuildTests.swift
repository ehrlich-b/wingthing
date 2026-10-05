import Foundation
import Testing

@Suite struct DeviceBuildTests {
    // make --dry-run expands recipes only; no build, provisioning, simulator or
    // device command is executed by these tests.
    private func recipe(_ target: String, bundle: String? = nil) throws -> String {
        let process = Process(), output = Pipe()
        process.executableURL = URL(fileURLWithPath: "/usr/bin/make")
        process.arguments = ["--dry-run", target]
        process.currentDirectoryURL = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
        var environment = ProcessInfo.processInfo.environment
        environment["DEVELOPMENT_TEAM"] = "TESTTEAM01"; environment["IOS_DEVICE_ID"] = "synthetic-device-id"
        environment["BUNDLE_ID"] = bundle; environment["IOS_DEVICE_CONFIGURATION"] = nil; environment["IOS_SIGNED_DERIVED_DATA"] = nil
        process.environment = environment; process.standardOutput = output; process.standardError = output
        try process.run()
        let data = output.fileHandleForReading.readDataToEndOfFile()
        process.waitUntilExit()
        expectEqual(process.terminationStatus, 0)
        return String(decoding: data, as: UTF8.self)
    }
    @Test func signedInstallUsesEnvironmentTeamAndDeviceAndSeparateSignedProduct() throws {
        let text = try recipe("device-install")
        for argument in ["DEVELOPMENT_TEAM=\"TESTTEAM01\"", "PRODUCT_BUNDLE_IDENTIFIER=\"dev.ehrlich.wingthing.TESTTEAM01\"",
            "CODE_SIGN_STYLE=Automatic", "CODE_SIGNING_ALLOWED=YES", "CODE_SIGNING_REQUIRED=YES", "CODE_SIGN_IDENTITY='Apple Development'",
            "-allowProvisioningUpdates", "-allowProvisioningDeviceRegistration", "platform=iOS,id=synthetic-device-id",
            "devicectl device install app --device \"synthetic-device-id\"", "device-build/Build/Products/Debug-iphoneos/Wingthing.app"] {
            expectTrue(text.contains(argument))
        }
        expectFalse(text.contains("device-unsigned-build"))
        let overridden = try recipe("device-build", bundle: "dev.ehrlich.wingthing.custom")
        expectTrue(overridden.contains("PRODUCT_BUNDLE_IDENTIFIER=\"dev.ehrlich.wingthing.custom\""))
    }
    @Test func simulatorAndUnsignedDeviceRecipesRetainDisabledSigning() throws {
        for target in ["simulator-build", "device-unsigned-check"] {
            let text = try recipe(target)
            expectTrue(text.contains("CODE_SIGNING_ALLOWED=NO CODE_SIGNING_REQUIRED=NO"))
            expectFalse(text.contains("-allowProvisioning")); expectFalse(text.contains("CODE_SIGN_STYLE=Automatic"))
        }
    }
}
