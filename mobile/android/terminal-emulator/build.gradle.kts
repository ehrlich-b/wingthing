plugins { id("com.android.library") }
android {
 namespace = "com.termux.terminal-emulator".replace("-", "_")
 buildToolsVersion = "37.0.0"
 compileSdk { version = release(37) { minorApiLevel = 0 } }
 defaultConfig { minSdk = 26 }
 compileOptions { sourceCompatibility = JavaVersion.VERSION_17; targetCompatibility = JavaVersion.VERSION_17 }
}
dependencies { implementation("androidx.annotation:annotation:1.9.1") }
