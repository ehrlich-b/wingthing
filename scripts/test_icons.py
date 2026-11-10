"""Small, offline icon contracts. Run with the pinned icon Python environment."""
from io import BytesIO
import json
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import unittest
import xml.etree.ElementTree as ET

from PIL import Image, ImageChops, ImageStat

ROOT = Path(__file__).resolve().parents[1]
BASE = "537b462e0ee577587af78061b4b4afe7b61eff90"
ANDROID = ROOT / "mobile/android/app/src/main"
A = "{http://schemas.android.com/apk/res/android}"


def git(*args):
    return subprocess.check_output(["git", "-C", str(ROOT), *args])


class Icons(unittest.TestCase):
    def test_reproducible_and_baseline(self):
        with tempfile.TemporaryDirectory() as scratch:
            target = Path(scratch)
            command = [sys.executable, str(ROOT / "scripts/generate-icons.py"), "--output-dir", str(target)]
            paths = subprocess.check_output(command, text=True).splitlines()
            first = {path: (target / path).read_bytes() for path in paths}
            subprocess.run(command, check=True, stdout=subprocess.DEVNULL)
            for path, expected in first.items():
                self.assertEqual((target / path).read_bytes(), expected, path + " second run")
                self.assertEqual((ROOT / path).read_bytes(), expected, path + " committed output")
                self.assertEqual(git("show", "HEAD:" + path), expected, path + " HEAD")
        old = Image.open(BytesIO(git("show", BASE + ":web/public/icons/wt-512.png"))).convert("RGB")
        new = Image.open(ROOT / "web/public/icons/wt-512.png").convert("RGB")
        self.assertEqual(new.size, (512, 512))
        delta = ImageChops.difference(old, new)
        mean = ImageStat.Stat(delta).mean
        # Different rasterizers affect only antialiased edges: <0.5/255 mean
        # error per channel and at least 99.5% of pixels within 32/255.
        outside = sum(max(pixel) > 32 for pixel in delta.getdata()) / (512 * 512)
        self.assertLess(max(mean), 0.5)
        self.assertLess(outside, 0.005)
        self.assertEqual(git("show", BASE + ":web/public/icons/wt.svg"), (ROOT / "web/public/icons/wt.svg").read_bytes())
        print("512px baseline channel MAE:", mean, "; pixels exceeding 32/255:", outside)

    def test_android_layers_and_safe_bounds(self):
        adaptive = ET.parse(ANDROID / "res/mipmap-anydpi-v26/ic_launcher.xml").getroot()
        self.assertEqual(adaptive.tag, "adaptive-icon")
        self.assertEqual([el.tag for el in adaptive], ["background", "foreground", "monochrome"])
        svg = ET.parse(ROOT / "web/public/icons/wt.svg").getroot()
        bounds = []
        for layer in adaptive:
            resource = layer.attrib[A + "drawable"].removeprefix("@drawable/")
            tree = ET.parse(ANDROID / "res/drawable" / (resource + ".xml")).getroot()
            if layer.tag == "background":
                self.assertEqual(tree.find("solid").attrib[A + "color"], svg[0].attrib["fill"])
                continue
            self.assertEqual(tree.attrib[A + "width"], "108dp")
            self.assertEqual(tree.attrib[A + "height"], "108dp")
            self.assertEqual(tree.attrib[A + "viewportWidth"], "108")
            self.assertEqual(tree.attrib[A + "viewportHeight"], "108")
            group = tree.find("group")
            self.assertEqual(len(group), 2)
            points = []
            for path, source in zip(group, svg[1:]):
                xy = [tuple(map(float, pair.split(","))) for pair in re.findall(r"[ML]([0-9.,]+)", path.attrib[A + "pathData"])]
                self.assertEqual(xy, [tuple(map(float, pair.split(","))) for pair in source.attrib["points"].split()])
                self.assertTrue(path.attrib[A + "pathData"].endswith("z"))
                self.assertEqual(path.attrib[A + "fillColor"], "#FFFFFF" if layer.tag == "monochrome" else source.attrib["fill"])
                points.extend((x * float(group.attrib[A + "scaleX"]) + float(group.attrib[A + "translateX"]),
                               y * float(group.attrib[A + "scaleY"]) + float(group.attrib[A + "translateY"])) for x, y in xy)
            self.assertTrue(all(21 <= x <= 87 and 21 <= y <= 87 for x, y in points))
            bounds.append((min(x for x, y in points), min(y for x, y in points), max(x for x, y in points), max(y for x, y in points)))
        self.assertEqual(bounds[0], bounds[1])
        app = ET.parse(ANDROID / "AndroidManifest.xml").getroot().find("application")
        for key in ("icon", "roundIcon"):
            self.assertEqual(app.attrib[A + key], "@mipmap/ic_launcher")
        print("Android foreground/monochrome bounds (dp):", bounds[0])

    def test_web_icons_and_small_blue(self):
        manifest = json.loads((ROOT / "web/public/manifest.json").read_text())
        self.assertEqual((ROOT / "web/public/manifest.json").read_bytes(), (ROOT / "web/manifest.json").read_bytes())
        for icon in manifest["icons"]:
            path = ROOT / "web/public" / icon["src"]
            if icon["type"] == "image/png":
                with Image.open(path) as image:
                    self.assertEqual(icon["sizes"], "%dx%d" % image.size)
                    self.assertEqual(image.convert("RGB").getpixel((0, 0)), (7, 19, 26))
        with Image.open(ROOT / "web/public/icons/wt-180.png") as touch:
            self.assertEqual(touch.size, (180, 180))
        html = (ROOT / "web/index.html").read_text()
        self.assertRegex(html, r'apple-touch-icon[^>]+icons/wt-180.png')
        with Image.open(ROOT / "web/public/favicon.ico") as icon:
            self.assertEqual(icon.ico.sizes(), {(n, n) for n in (16, 29, 32, 48, 64)})
            small = icon.ico.getimage((29, 29)).convert("RGB")
            distance = min(max(abs(a - b) for a, b in zip(pixel, (42, 168, 255))) for pixel in small.getdata())
            self.assertLessEqual(distance, 3)
        print("29px favicon nearest blue channel distance:", distance)


if __name__ == "__main__":
    unittest.main(verbosity=2)
