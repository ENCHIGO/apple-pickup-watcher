"""Validate the built site's crawl paths offline; no third-party dependencies."""
import json
from html.parser import HTMLParser
from pathlib import Path
from urllib.parse import unquote, urljoin, urlsplit
import xml.etree.ElementTree as ET

ROOT = Path(__file__).resolve().parents[1] / "_site"


class Page(HTMLParser):
    def __init__(self, path):
        super().__init__(convert_charrefs=True)
        self.path = path
        self.links, self.meta, self.ids, self.schemas = [], {}, set(), []
        self.h1, self.language, self.canonical, self.alternates = 0, "", "", {}
        self.script = None
        self.script_text = ""
        self.text = []
        self.feed(path.read_text())

    def handle_starttag(self, tag, attrs):
        a = dict(attrs)
        if "id" in a:
            assert a["id"] not in self.ids, f"Duplicate id in {self.path}: {a['id']}"
            self.ids.add(a["id"])
        if tag == "html":
            self.language = a.get("lang", "")
        if tag == "h1":
            self.h1 += 1
        if tag == "meta":
            self.meta[a.get("name", a.get("property", ""))] = a.get("content", "")
        if tag == "link" and a.get("rel") == "canonical":
            assert not self.canonical, "Duplicate canonical"
            self.canonical = a["href"]
        if tag == "link" and a.get("rel") == "alternate":
            self.alternates[a["hreflang"]] = a["href"]
        for key in ("href", "src"):
            if key in a:
                self.links.append(a[key])
        if tag == "img":
            assert "alt" in a and "width" in a and "height" in a, f"Image missing accessibility or size: {a}"
        if tag == "script":
            self.script = a.get("type", "javascript")
            assert self.script == "application/ld+json", "Landing content must work without JS"

    def handle_data(self, data):
        if self.script:
            self.script_text += data
        else:
            self.text.append(data)

    def handle_endtag(self, tag):
        if tag == "script":
            self.schemas.append(json.loads(self.script_text))
            self.script = None
            self.script_text = ""


def check():
    files = sorted(ROOT.rglob("*.html"))
    assert len(files) == 2, f"Expected two language pages, got {files}"
    pages = [Page(p) for p in files]
    by_url = {p.canonical: p for p in pages}
    base = next(p.canonical for p in pages if p.language == "zh-CN")
    base_parts = urlsplit(base)
    assert base_parts.scheme == "https" and base.endswith("/")
    descriptions = set()
    for p in pages:
        assert p.h1 == 1 and p.language in {"zh-CN", "en"}
        assert p.canonical == urljoin(base, str(p.path.relative_to(ROOT)).removesuffix("index.html"))
        assert p.meta["description"] and "noindex" not in p.meta["robots"]
        descriptions.add(p.meta["description"])
        assert p.meta["og:url"] == p.canonical
        assert p.meta["og:description"] == p.meta["description"]
        assert p.meta["twitter:card"] == "summary_large_image"
        expected = {q.language: q.canonical for q in pages} | {"x-default": base}
        assert p.alternates == expected, f"Incomplete reciprocal hreflang: {p.path}"
        visible = " ".join(p.text)
        graph = p.schemas[0]["@graph"]
        faq = next(n for n in graph if n["@type"] == "FAQPage")
        assert len(faq["mainEntity"]) >= 5
        for q in faq["mainEntity"]:
            assert q["name"] in visible and q["acceptedAnswer"]["text"] in visible
        for ref in p.links + [p.meta["og:image"], p.meta["twitter:image"]]:
            resolved = urlsplit(urljoin(p.canonical, ref))
            if resolved.netloc != base_parts.netloc:
                continue
            assert resolved.path.startswith(base_parts.path), f"Link escapes site base: {ref}"
            relative = unquote(resolved.path[len(base_parts.path):])
            target = ROOT / relative
            if target.is_dir():
                target /= "index.html"
            assert target.is_file(), f"Missing internal target: {ref} from {p.path}"
            if resolved.fragment:
                target_url = resolved._replace(fragment="").geturl()
                assert unquote(resolved.fragment) in by_url[target_url].ids, f"Missing fragment: {ref}"
    assert len(descriptions) == len(pages), "Each language needs its own description"
    ns = {"s": "http://www.sitemaps.org/schemas/sitemap/0.9", "x": "http://www.w3.org/1999/xhtml"}
    sitemap = ET.parse(ROOT / "sitemap.xml").getroot()
    assert {n.text for n in sitemap.findall("s:url/s:loc", ns)} == set(by_url)
    for entry in sitemap.findall("s:url", ns):
        assert {n.attrib["hreflang"]: n.attrib["href"] for n in entry.findall("x:link", ns)} == expected
    png = (ROOT / "assets/social.png").read_bytes()
    assert png[:8] == b"\x89PNG\r\n\x1a\n"
    assert int.from_bytes(png[16:20], "big") == 1200 and int.from_bytes(png[20:24], "big") == 630
    assert (ROOT / ".nojekyll").exists()
    assert base in (ROOT / "llms.txt").read_text()
    if base_parts.path == "/":
        assert f"Sitemap: {base}sitemap.xml" in (ROOT / "robots.txt").read_text()
    else:
        assert not (ROOT / "robots.txt").exists(), "A subpath robots.txt cannot control crawlers"
    print(f"OK: {len(pages)} pages; links, language alternates, visible schema, sitemap and social image")


if __name__ == "__main__":
    check()
