# Android release artifacts

Set up the shared or local SDK as described in [TOOLCHAIN.md](TOOLCHAIN.md),
then build the Go bridge once with `./build-bridge.sh`. From this directory:

```sh
unset WT_SIGNING_PASSWORD WT_SIGNING_STORE
./build-release.sh -PwtBuildNumber=2
```

This runs unit tests and lint and produces `app/build/outputs/apk/release/app-release-unsigned.apk`
and `app/build/outputs/bundle/release/app-release.aab`. `wtBuildNumber` sets
Android's versionCode (default `1`); versionName remains `0.1.0`. Increase the
build number for each upload. Gradle signs a release only when both
`WT_SIGNING_STORE` and `WT_SIGNING_PASSWORD` are set.

Sign the unsigned artifacts on the machine holding the release key:

```sh
./sign-release.sh app/build/outputs/apk/release/app-release-unsigned.apk \
  app/build/outputs/bundle/release/app-release.aab \
  /path/to/upload.p12 /path/to/password-file /path/to/new-output-directory
```

The PKCS12 key alias defaults to `wingthing`; set `WT_SIGNING_ALIAS` for another
alias. Use a private password file containing the password on its
first line, with permissions `0600`; the store and key must use that same
password. The script passes the file directly to the
signing tools. It zipaligns the APK for 16 KB pages, signs with APK v2 and v3,
and jarsigns the AAB. It verifies both outputs and refuses to overwrite existing
`wingthing-release.apk` or `wingthing-release.aab` files. Only the signed APK
can be installed directly on a phone; the signed AAB is for a Play upload.
