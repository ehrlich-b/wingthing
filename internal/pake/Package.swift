// swift-tools-version: 6.0
import PackageDescription
let package = Package(name: "WingthingPAKE", products: [.library(name: "WingthingPAKE", targets: ["CCryptoBoringSSL"])], targets: [
    .target(name: "CCryptoBoringSSL", path: "boringssl", exclude: ["CMakeLists.txt", "hash.txt", "PrivacyInfo.xcprivacy", "crypto/bio/connect.cc", "crypto/bio/socket_helper.cc", "crypto/bio/socket.cc"], publicHeadersPath: "include", cSettings: [.define("OPENSSL_NO_ASM")])
], cxxLanguageStandard: .cxx17)
