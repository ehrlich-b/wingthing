.PHONY: core-test ui-typecheck check

IOS_SCRATCH ?= /tmp/wingthing-ios-swift-build
IOS_CACHE ?= /tmp/wingthing-ios-swift-cache
IOS_CONFIG ?= /tmp/wingthing-ios-spm-config
IOS_SECURITY ?= /tmp/wingthing-ios-spm-security
SWIFT_FLAGS = --scratch-path $(IOS_SCRATCH) --cache-path $(IOS_CACHE) --config-path $(IOS_CONFIG) --security-path $(IOS_SECURITY)
export CLANG_MODULE_CACHE_PATH ?= /tmp/wingthing-ios-clang-cache
export SWIFTPM_MODULECACHE_OVERRIDE ?= /tmp/wingthing-ios-module-cache

core-test:
	cd WingthingCore && swift test --disable-xctest --enable-swift-testing $(SWIFT_FLAGS)

# This checks the native SwiftUI library against the installed Mac SDK. It does
# not compile an iOS app or establish simulator/device acceptance.
ui-typecheck:
	cd WingthingCore && swift build --target WingthingUI $(SWIFT_FLAGS)

check: core-test ui-typecheck
