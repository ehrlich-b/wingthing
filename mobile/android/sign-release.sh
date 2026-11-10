#!/usr/bin/env bash
# Keep passwords in a file, including when the caller enables shell tracing.
set +x
set -euo pipefail
if [[ $# != 5 ]]; then
  echo 'Usage: sign-release.sh <unsigned.apk> <unsigned.aab> <keystore.p12> <password-file> <out-dir>' >&2
  exit 2
fi
source "$(dirname "$0")/toolchain.sh"
apk=$1
aab=$2
keystore=$3
password_file=$4
out_dir=$5
for input in "$apk" "$aab" "$keystore" "$password_file"; do
  [[ -f "$input" && -r "$input" ]] || { echo "Unreadable input: $input" >&2; exit 1; }
done
mkdir -p "$out_dir"
signed_apk="$out_dir/wingthing-release.apk"
signed_aab="$out_dir/wingthing-release.aab"
for output in "$signed_apk" "$signed_aab"; do
  [[ ! -e "$output" ]] || { echo "Output already exists: $output" >&2; exit 1; }
done
build_tools="$ANDROID_HOME/build-tools/37.0.0"
work_dir=$(mktemp -d "$out_dir/.sign-release.XXXXXX")
trap 'rm -rf -- "$work_dir"' EXIT
"$build_tools/zipalign" -P 16 -f 4 "$apk" "$work_dir/aligned.apk"
"$build_tools/apksigner" sign --ks "$keystore" --ks-type PKCS12 \
  --ks-key-alias "${WT_SIGNING_ALIAS:-wingthing}" --ks-pass "file:$password_file" \
  --key-pass "file:$password_file" --v1-signing-enabled false \
  --v2-signing-enabled true --v3-signing-enabled true \
  --out "$signed_apk" "$work_dir/aligned.apk"
jarsigner -keystore "$keystore" -storetype PKCS12 -storepass:file "$password_file" \
  -keypass:file "$password_file" -digestalg SHA-256 \
  -signedjar "$signed_aab" "$aab" "${WT_SIGNING_ALIAS:-wingthing}"
"$build_tools/zipalign" -c -P 16 4 "$signed_apk"
"$build_tools/apksigner" verify "$signed_apk"
jarsigner -verify "$signed_aab"
printf 'Signed APK: %s\nSigned AAB: %s\n' "$signed_apk" "$signed_aab"
