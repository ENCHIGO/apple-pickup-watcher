import { cp, mkdir, rm, writeFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { site, locales } from "../site/content.mjs";

const root = new URL("../", import.meta.url);
const output = new URL("_site/", root);
const base = new URL(process.env.SITE_URL || site.url);
if (base.protocol !== "https:" || base.search || base.hash || base.username || base.password) {
  throw new Error("SITE_URL must be a public HTTPS URL without credentials, query or fragment");
}
if (!base.pathname.endsWith("/")) base.pathname += "/";
const url = (path = "") => new URL(path, base).href;
const asset = (path) => `${base.pathname}${path}`;
const escape = (text) => text.replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]);
const json = (value) => JSON.stringify(value).replace(/</g, "\\u003c");
const repo = site.repository;
const releases = `${repo}/releases/latest`;
const readme = `${repo}#安装`;

function render(c) {
  const canonical = url(c.path);
  const structuredData = {
    "@context": "https://schema.org",
    "@graph": [
      { "@type": "WebSite", "@id": `${url()}#website`, name: site.name, url: url(), inLanguage: locales.map((l) => l.lang) },
      { "@type": "SoftwareApplication", "@id": `${url()}#software`, name: site.name,
        url: url(), description: c.intro, applicationCategory: "UtilitiesApplication",
        operatingSystem: ["macOS", "Windows", "Linux"], isAccessibleForFree: true,
        license: "https://www.gnu.org/licenses/gpl-3.0.html", downloadUrl: releases,
        sameAs: repo, author: { "@type": "Person", name: "ENCHIGO", url: "https://github.com/ENCHIGO" },
        offers: { "@type": "Offer", price: "0", priceCurrency: "USD" },
        featureList: c.features.map((f) => f[2]),
      },
      { "@type": "WebPage", "@id": `${canonical}#page`, url: canonical, name: c.title,
        description: c.description, inLanguage: c.lang, isPartOf: { "@id": `${url()}#website` },
        about: { "@id": `${url()}#software` },
      },
      { "@type": "FAQPage", "@id": `${canonical}#faq`, inLanguage: c.lang,
        isPartOf: { "@id": `${canonical}#page` }, mainEntity: c.faqs.map(([q, a]) => ({
          "@type": "Question", name: q, acceptedAnswer: { "@type": "Answer", text: a },
        })),
      },
    ],
  };
  return `<!doctype html>
<html lang="${c.lang}">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>${escape(c.title)}</title>
  <meta name="description" content="${escape(c.description)}">
  <meta name="robots" content="index, follow, max-image-preview:large">
  <meta name="theme-color" content="#eff1e9">
  <link rel="canonical" href="${canonical}">
  ${locales.map((l) => `<link rel="alternate" hreflang="${l.lang}" href="${url(l.path)}">`).join("\n  ")}
  <link rel="alternate" hreflang="x-default" href="${url()}">
  <link rel="sitemap" type="application/xml" href="${url("sitemap.xml")}">
  <link rel="icon" type="image/svg+xml" href="${asset("assets/mark.svg")}">
  <link rel="stylesheet" href="${asset("assets/style.css")}">
  <meta property="og:type" content="website">
  <meta property="og:site_name" content="${site.name}">
  <meta property="og:title" content="${escape(c.title)}">
  <meta property="og:description" content="${escape(c.description)}">
  <meta property="og:url" content="${canonical}">
  <meta property="og:locale" content="${c.ogLocale}">
  ${locales.filter((l) => l !== c).map((l) => `<meta property="og:locale:alternate" content="${l.ogLocale}">`).join("\n  ")}
  <meta property="og:image" content="${url("assets/social.png")}">
  <meta property="og:image:width" content="1200">
  <meta property="og:image:height" content="630">
  <meta property="og:image:alt" content="Apple Pickup Watcher — Apple Store pickup stock alerts for macOS, Windows and Linux">
  <meta name="twitter:card" content="summary_large_image">
  <meta name="twitter:title" content="${escape(c.title)}">
  <meta name="twitter:description" content="${escape(c.description)}">
  <meta name="twitter:image" content="${url("assets/social.png")}">
  <meta name="twitter:image:alt" content="Apple Pickup Watcher — Apple Store pickup stock alerts for macOS, Windows and Linux">
  <script type="application/ld+json">${json(structuredData)}</script>
</head>
<body>
  <a class="skip" href="#main">${c.skip}</a>
  <header class="header wrap">
    <a class="brand" href="${asset(c.path)}"><img src="${asset("assets/mark.svg")}" width="32" height="32" alt=""><span>Apple Pickup<br>Watcher<span class="brand-dot">.</span></span></a>
    <nav aria-label="${c.lang === "en" ? "Main navigation" : "主导航"}">
      <a href="#features">${c.nav[0]}</a><a href="#download">${c.nav[1]}</a><a href="#faq">${c.nav[2]}</a>
      <a class="language" href="${asset(c.switchPath)}" lang="${c.lang === "en" ? "zh-CN" : "en"}" hreflang="${c.lang === "en" ? "zh-CN" : "en"}">${c.switchLabel} ↗</a>
    </nav>
  </header>
  <main id="main">
    <section class="hero wrap">
      <div class="hero-copy">
        <p class="eyebrow"><span class="live-dot"></span>${c.eyebrow}</p>
        <h1>${c.heading}<br><em>${c.headingAccent}</em></h1>
        <p class="lead">${c.lead}</p>
        <div class="actions"><a class="button" href="#download">${c.download} <span aria-hidden="true">↗</span></a><a class="text-link" href="#how-it-works">${c.how} ↓</a></div>
        <p class="platforms">${c.platforms}</p>
      </div>
      <figure class="signal">
        <div class="orbit orbit-one"></div><div class="orbit orbit-two"></div>
        <div class="signal-inner">
          <div class="signal-top"><span class="monogram">APW</span><span class="live-dot"></span></div>
          <p class="signal-store">${c.demoStore}</p><h2>${c.demoProduct}</h2>
          <div class="availability"><span class="live-dot"></span>${c.demoAvailable}<span aria-hidden="true">↗</span></div>
          <div class="notification"><span class="bell" aria-hidden="true">↗</span><div><strong>${c.demoAlert}</strong><p>${c.demoBody}</p></div></div>
        </div>
        <figcaption>${c.demoLabel}</figcaption>
      </figure>
    </section>
    <div class="ribbon"><div class="wrap">${c.ribbon.map((s) => `<span>${s}</span>`).join("")}</div></div>
    <section id="features" class="section wrap">
      <p class="eyebrow">${c.introLabel}</p><div class="section-intro"><h2>${c.introHeading}</h2><p>${c.intro}</p></div>
      <div class="features">${c.features.map(([n, title, text]) => `<article><span class="number">${n}</span><h3>${title}</h3><p>${text}</p></article>`).join("")}</div>
      <div class="coverage"><h3>${c.coverageHeading}</h3><dl><dt>${c.categoriesLabel}</dt><dd>${c.categories}</dd><dt>${c.regionsLabel}</dt><dd>${c.regions}</dd></dl><p>${c.coverageNote}</p></div>
    </section>
    <section id="how-it-works" class="steps-section"><div class="wrap section"><h2>${c.stepsHeading}</h2><ol class="steps">${c.steps.map(([title, text]) => `<li><h3>${title}</h3><p>${text}</p></li>`).join("")}</ol></div></section>
    <section id="download" class="section wrap download"><div><p class="eyebrow">APPLE PICKUP WATCHER</p><h2>${c.downloadHeading}</h2><p>${c.downloadLead}</p><a class="button" href="${releases}">${c.releaseNotes} ↗</a></div><div><ul class="packages">${c.packages.map(([name, detail]) => `<li><a href="${releases}"><strong>${name}</strong><span>${detail}</span><span aria-hidden="true">↗</span></a></li>`).join("")}</ul><p class="install-note">${c.installNote} <a href="${readme}">${c.installGuide} ↗</a></p></div></section>
    <section id="faq" class="section wrap faq"><h2>${c.faqHeading}</h2><div>${c.faqs.map(([q, a], i) => `<details id="faq-${i + 1}"><summary>${escape(q)}</summary><p>${escape(a)}</p></details>`).join("")}</div></section>
    <section class="evidence wrap"><h2>${c.evidenceHeading}</h2><p>${c.evidenceText}</p><ul>${[repo + "#readme", repo + "/releases", repo + "/issues", repo + "/blob/main/NOTICE"].map((href, i) => `<li><a href="${href}">${c.evidenceLinks[i]} ↗</a></li>`).join("")}</ul></section>
  </main>
  <footer class="footer wrap"><p>${c.footer}</p><div><a href="${repo}">GitHub</a><a href="${repo}/blob/main/LICENSE">GPL-3.0-or-later</a><a href="${asset("llms.txt")}">${c.facts}</a><a href="${asset("sitemap.xml")}">${c.sitemap}</a></div></footer>
</body>
</html>
`;
}

await rm(output, { recursive: true, force: true });
await mkdir(output, { recursive: true });
await cp(new URL("site/assets/", root), new URL("assets/", output), { recursive: true });
for (const c of locales) {
  const dir = new URL(c.path, output);
  await mkdir(dir, { recursive: true });
  await writeFile(new URL("index.html", dir), render(c));
}
await writeFile(new URL(".nojekyll", output), "");
await writeFile(new URL("sitemap.xml", output), `<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9" xmlns:xhtml="http://www.w3.org/1999/xhtml">
${locales.map((c) => `  <url><loc>${escape(url(c.path))}</loc>${locales.map((l) => `<xhtml:link rel="alternate" hreflang="${l.lang}" href="${escape(url(l.path))}"/>`).join("")}<xhtml:link rel="alternate" hreflang="x-default" href="${escape(url())}"/></url>`).join("\n")}
</urlset>
`);
// A project Pages URL is under /apple-pickup-watcher/. A robots.txt there has
// no crawler authority; only emit one when deployment owns the hostname root.
if (base.pathname === "/") {
  await writeFile(new URL("robots.txt", output), `User-agent: *\nAllow: /\n\nSitemap: ${url("sitemap.xml")}\n`);
}
await writeFile(new URL("llms.txt", output), `# Apple Pickup Watcher

> Free, open-source desktop monitoring for Apple Retail Store pickup availability. 苹果直营店到店取货库存监控与到货提醒。

## Documentation

- [简体中文介绍与常见问题](${url()}): 支持范围、安装入口、使用方式和限制。
- [English overview and FAQ](${url("en/")}): Supported products, regions, platforms, notifications and limitations.
- [Source and README](${repo}): Implementation and full setup instructions.
- [Latest stable release](${releases}): Current published version and platform installers; check release notes for changes.
- [Issues](${repo}/issues): Reported problems and unresolved environment-specific behavior.
- [Attribution](${repo}/blob/main/NOTICE): Rewrite of hteen/apple-store-helper.
- [License](${repo}/blob/main/LICENSE): GPL-3.0-or-later.

## Facts and limitations

- Products: iPhone, iPad, Mac and Apple Watch. Individual models depend on the app catalog.
- Regions: China mainland, Hong Kong, Taiwan, Japan, Singapore, Australia and Malaysia.
- Platforms: macOS (Apple Silicon and Intel), Windows x64 and Linux x86_64.
- Alerts: desktop notification, sound and optional Bark push. The app must stay running; the computer must stay awake and online.
- Unknown or failed queries are not out-of-stock results. HTTP 541 alone does not establish its cause or inventory status.
- Default polling interval: 30 seconds. Region support does not guarantee successful queries.
- The app can open a shopping bag, but users add items, select pickup stores, check out and pay manually on Apple's website. No reservation or purchase guarantee.
- Independent project, not affiliated with or endorsed by Apple Inc.

This is an optional factual reading index. It is not a crawler access policy or a guarantee of search indexing or AI citation.
`);
console.log(`Built ${locales.length} static pages in ${fileURLToPath(output)} for ${base.href}`);
