.PHONY: core-test ui-typecheck check live-preview-build live-preview live-preview-release-check simulator-preflight simulator-build simulator-ui-build simulator-ui-check simulator-fixture-check simulator-visual-check simulator-local-state-check simulator-first-connection-check simulator-creation-check device-unsigned-check

IOS_SCRATCH ?= /tmp/wingthing-ios-swift-build
IOS_CACHE ?= /tmp/wingthing-ios-swift-cache
IOS_CONFIG ?= /tmp/wingthing-ios-spm-config
IOS_SECURITY ?= /tmp/wingthing-ios-spm-security
SWIFT_FLAGS = --scratch-path $(IOS_SCRATCH) --cache-path $(IOS_CACHE) --config-path $(IOS_CONFIG) --security-path $(IOS_SECURITY) --jobs 2
export CLANG_MODULE_CACHE_PATH ?= /tmp/wingthing-ios-clang-cache
export SWIFTPM_MODULECACHE_OVERRIDE ?= /tmp/wingthing-ios-module-cache

core-test:
	cd WingthingCore && swift test --disable-xctest --enable-swift-testing $(SWIFT_FLAGS)

# This checks the native SwiftUI library against the installed Mac SDK. It does
# not compile an iOS app or establish simulator/device acceptance.
ui-typecheck:
	cd WingthingCore && swift build --target WingthingUI $(SWIFT_FLAGS)

check: core-test ui-typecheck

# Debug-only, read-only acceptance against an explicit same-Mac preview roost.
# Every identity is required; there is no credential argument.
live-preview-build:
	cd WingthingCore && swift build --configuration debug --product WingthingLivePreview $(SWIFT_FLAGS)

live-preview: live-preview-build
	@test -n "$(PREVIEW_ORIGIN)" -a -n "$(PREVIEW_USER)" -a -n "$(PREVIEW_WING)" -a -n "$(PREVIEW_KEY)" -a -n "$(PREVIEW_ROOT)" || { echo "PREVIEW_ORIGIN, PREVIEW_USER, PREVIEW_WING, PREVIEW_KEY and PREVIEW_ROOT are required" >&2; exit 2; }
	cd WingthingCore && "$$(swift build --configuration debug --show-bin-path $(SWIFT_FLAGS))/WingthingLivePreview" --origin "$(PREVIEW_ORIGIN)" --user "$(PREVIEW_USER)" --wing "$(PREVIEW_WING)" --public-key "$(PREVIEW_KEY)" --root "$(PREVIEW_ROOT)"

live-preview-release-check:
	cd WingthingCore && swift build --configuration release --product WingthingLivePreview $(SWIFT_FLAGS)
	@cd WingthingCore && result="$$("$$(swift build --configuration release --show-bin-path $(SWIFT_FLAGS))/WingthingLivePreview" 2>&1)"; status=$$?; test $$status -eq 2 && test "$$result" = 'WingthingLivePreview is available only in debug builds.'

# The human installs Xcode, accepts its license and selects components first.
# DEVELOPER_DIR is scoped to this invocation; xcode-select is never changed.
IOS_DERIVED_DATA ?= /tmp/wingthing-ios-simulator-build
XCODEBUILD_FLAGS ?= -jobs 2
simulator-preflight:
	@test -n "$$DEVELOPER_DIR" && test -x "$$DEVELOPER_DIR/usr/bin/xcodebuild" || { echo 'DEVELOPER_DIR must name an installed full Xcode Contents/Developer directory' >&2; exit 2; }
	@xcodebuild -version
	@xcodebuild -checkFirstLaunchStatus
	@xcrun --sdk iphonesimulator --show-sdk-path

simulator-build: simulator-preflight
	xcodebuild $(XCODEBUILD_FLAGS) -project Wingthing.xcodeproj -scheme Wingthing -configuration Debug -sdk iphonesimulator -destination 'generic/platform=iOS Simulator' -derivedDataPath "$(IOS_DERIVED_DATA)" CODE_SIGNING_ALLOWED=NO CODE_SIGNING_REQUIRED=NO build

# Device-SDK compilation only. No team, registration, provisioning, install or
# signing action is accepted; the resulting unsigned app cannot run on a phone.
IOS_DEVICE_DERIVED_DATA ?= /tmp/wingthing-ios-device-unsigned-build
device-unsigned-check: simulator-preflight
	@xcrun --sdk iphoneos --show-sdk-path
	xcodebuild $(XCODEBUILD_FLAGS) -project Wingthing.xcodeproj -scheme Wingthing -configuration Release -sdk iphoneos -destination 'generic/platform=iOS' -derivedDataPath "$(IOS_DEVICE_DERIVED_DATA)" CODE_SIGNING_ALLOWED=NO CODE_SIGNING_REQUIRED=NO OTHER_SWIFT_FLAGS='$$(inherited) -j2' build

# Use an explicitly selected, dedicated simulator. These tests never send or
# stop an existing conversation; their preview inputs contain no credentials.
IOS_QA_DEVICE ?=
IOS_QA_CHILD ?=
IOS_QA_RESULTS ?= /tmp/wingthing-ios-ui-acceptance
simulator-ui-build: simulator-preflight
	@test -n "$(IOS_QA_DEVICE)" || { echo 'IOS_QA_DEVICE must name the dedicated simulator UUID' >&2; exit 2; }
	xcodebuild $(XCODEBUILD_FLAGS) -project Wingthing.xcodeproj -scheme Wingthing -configuration Debug -sdk iphonesimulator -destination 'platform=iOS Simulator,id=$(IOS_QA_DEVICE)' -derivedDataPath "$(IOS_DERIVED_DATA)" CODE_SIGNING_ALLOWED=NO CODE_SIGNING_REQUIRED=NO ONLY_ACTIVE_ARCH=YES OTHER_SWIFT_FLAGS='$$(inherited) -j2' build-for-testing

simulator-ui-check: simulator-ui-build
	python3 scripts/simulator_ui_acceptance.py --device "$(IOS_QA_DEVICE)" --derived-data "$(IOS_DERIVED_DATA)" --results "$(IOS_QA_RESULTS)" --origin "$(PREVIEW_ORIGIN)" --user "$(PREVIEW_USER)" --wing "$(PREVIEW_WING)" --key "$(PREVIEW_KEY)" --root "$(PREVIEW_ROOT)" --child "$(IOS_QA_CHILD)"

# Send/replay/Stop operate only on an in-process synthetic encrypted home.
# The separate inspection check above continues to read existing tasks only.
simulator-fixture-check: simulator-ui-build
	python3 scripts/simulator_fixture_acceptance.py --device "$(IOS_QA_DEVICE)" --derived-data "$(IOS_DERIVED_DATA)" --results "$(IOS_QA_RESULTS)"

# Normal system text and device themes, with synthetic native child navigation.
IOS_QA_APPEARANCE ?= dark
simulator-visual-check: simulator-ui-build
	python3 scripts/simulator_fixture_acceptance.py --device "$(IOS_QA_DEVICE)" --derived-data "$(IOS_DERIVED_DATA)" --results "$(IOS_QA_RESULTS)" --suite visual --appearance "$(IOS_QA_APPEARANCE)"

# Relaunch and exact message/within-message restoration use a fresh synthetic
# test UUID; no real home, bearer, or pending provider execution is restored.
simulator-local-state-check: simulator-ui-build
	python3 scripts/simulator_fixture_acceptance.py --device "$(IOS_QA_DEVICE)" --derived-data "$(IOS_DERIVED_DATA)" --results "$(IOS_QA_RESULTS)" --suite persistence

simulator-first-connection-check: simulator-ui-build
	python3 scripts/simulator_fixture_acceptance.py --device "$(IOS_QA_DEVICE)" --derived-data "$(IOS_DERIVED_DATA)" --results "$(IOS_QA_RESULTS)" --suite first-connection

simulator-creation-check: simulator-ui-build
	python3 scripts/simulator_fixture_acceptance.py --device "$(IOS_QA_DEVICE)" --derived-data "$(IOS_DERIVED_DATA)" --results "$(IOS_QA_RESULTS)" --suite creation
