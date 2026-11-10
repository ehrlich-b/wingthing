plugins { id("com.android.application"); id("org.jetbrains.kotlin.plugin.compose") }
val uploadStore = System.getenv("WT_SIGNING_STORE")?.takeIf { it.isNotBlank() }
val uploadPassword = System.getenv("WT_SIGNING_PASSWORD")?.takeIf { it.isNotBlank() }
val hasUploadSigning = uploadStore != null && uploadPassword != null
android {
 namespace = "ai.wingthing.phone"
 compileSdk { version = release(37) { minorApiLevel = 0 } }
 buildToolsVersion = "37.0.0"
 defaultConfig {
  applicationId = "ai.wingthing.phone"
  minSdk = 26
  targetSdk = 37
  versionCode = providers.gradleProperty("wtBuildNumber").orElse("1").get().toInt()
  versionName = "0.1.0"
  testInstrumentationRunner = "androidx.test.runner.AndroidJUnitRunner"
  ndk { abiFilters += listOf("arm64-v8a", "x86_64") }
 }
 signingConfigs {
  if (hasUploadSigning) { create("upload") {
   storeFile = rootProject.file(uploadStore!!)
   storePassword = uploadPassword
   keyAlias = "wingthing"
   keyPassword = uploadPassword
  } }
 }
 buildTypes { release {
  if (hasUploadSigning) { signingConfig = signingConfigs.getByName("upload") }
  isMinifyEnabled = false
 } }
 buildFeatures { compose = true; buildConfig = true }
 compileOptions { sourceCompatibility = JavaVersion.VERSION_17; targetCompatibility = JavaVersion.VERSION_17 }
 packaging { resources.excludes += listOf("META-INF/AL2.0", "META-INF/LGPL2.1") }
 lint { abortOnError = true }
}
dependencies {
 implementation(files("libs/wingthing.aar"))
 implementation(project(":terminal-view"))
 implementation(platform("androidx.compose:compose-bom:2026.09.00"))
 implementation("androidx.compose.material3:material3")
 implementation("androidx.compose.ui:ui-tooling-preview")
 implementation("androidx.activity:activity-compose:1.13.0")
 implementation("androidx.lifecycle:lifecycle-viewmodel-compose:2.10.0")
 implementation("androidx.lifecycle:lifecycle-runtime-compose:2.10.0")
 implementation("com.squareup.okhttp3:okhttp:4.12.0")
 implementation("com.journeyapps:zxing-android-embedded:4.3.0")
 testImplementation("junit:junit:4.13.2")
 androidTestImplementation(platform("androidx.compose:compose-bom:2026.09.00"))
 androidTestImplementation("androidx.compose.ui:ui-test-junit4")
 androidTestImplementation("androidx.test.ext:junit:1.3.0")
 androidTestImplementation("androidx.test:runner:1.7.0")
 androidTestImplementation("androidx.test:rules:1.7.0")
 androidTestImplementation("androidx.test.uiautomator:uiautomator:2.3.0")
 debugImplementation("androidx.compose.ui:ui-test-manifest")
}
