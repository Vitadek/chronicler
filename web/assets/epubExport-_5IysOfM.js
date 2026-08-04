import{J as u}from"./jszip.min-DndVDAcI.js";import{a as v}from"./FileSaver.min-ZLU22xt5.js";import{s as b,f as w}from"./exportSanitize-CklZIBL6.js";import{b1 as $}from"./index-Cx86ZJP0.js";import"./jszip.min-BfFVIawh.js";function T(e,t){const i=r=>r.replace(/&/g,"&amp;").replace(/</g,"&lt;").replace(/>/g,"&gt;").replace(/"/g,"&quot;").replace(/'/g,"&apos;"),o=i(e),n=i(t);return`<?xml version="1.0" encoding="UTF-8"?>
<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1600 2400" width="1600" height="2400">
  <defs>
    <linearGradient id="g" x1="0" y1="0" x2="1" y2="1">
      <stop offset="0%" stop-color="#1c1b1a"/>
      <stop offset="100%" stop-color="#3a3735"/>
    </linearGradient>
  </defs>
  <rect width="1600" height="2400" fill="url(#g)"/>
  <line x1="200" y1="600" x2="1400" y2="600" stroke="#c9b896" stroke-width="2"/>
  <line x1="200" y1="1800" x2="1400" y2="1800" stroke="#c9b896" stroke-width="2"/>
  <text x="800" y="1100" text-anchor="middle" fill="#f1ede4"
        font-family="Georgia, serif" font-size="120" font-style="italic">${o}</text>
  <text x="800" y="1300" text-anchor="middle" fill="#c9b896"
        font-family="Georgia, serif" font-size="64" font-style="italic">by ${n}</text>
  <text x="800" y="2200" text-anchor="middle" fill="#9a8d75"
        font-family="Georgia, serif" font-size="36" letter-spacing="12">CHRONICLER</text>
</svg>`}function C(){return new Date().toISOString().replace(/\.\d{3}Z$/,"Z")}const h='<?xml version="1.0" encoding="UTF-8"?>';function k(e,t){return`${h}
<!DOCTYPE html>
<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops" lang="en">
<head>
  <meta charset="utf-8"/>
  <title>${e.replace(/&/g,"&amp;").replace(/</g,"&lt;")}</title>
  <link rel="stylesheet" type="text/css" href="../style.css"/>
</head>
<body>
  <section epub:type="chapter">
    <h2 class="chapter-title">${e.replace(/&/g,"&amp;").replace(/</g,"&lt;")}</h2>
    ${t}
  </section>
</body>
</html>`}function E(e,t){return`${h}
<!DOCTYPE html>
<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops" lang="en">
<head>
  <meta charset="utf-8"/>
  <title>Cover</title>
  <link rel="stylesheet" type="text/css" href="style.css"/>
  <style>
    body { margin: 0; padding: 0; }
    .cover { display: flex; align-items: center; justify-content: center; height: 100vh; }
    .cover img { max-width: 100%; max-height: 100%; }
  </style>
</head>
<body>
  <section epub:type="cover">
    <div class="cover">
      <img src="${e}" alt="Cover of ${t.replace(/"/g,"&quot;")}"/>
    </div>
  </section>
</body>
</html>`}function O(e,t,i){const o=new Date().getFullYear(),n=i?i.split(`
`).map(r=>`<p>${r.replace(/&/g,"&amp;").replace(/</g,"&lt;")}</p>`).join(`
      `):`<p>Copyright © ${o} ${t.replace(/&/g,"&amp;").replace(/</g,"&lt;")}. All rights reserved.</p>
      <p>This is a work of fiction. Names, characters, businesses, places, events, locales, and incidents are either the products of the author's imagination or used in a fictitious manner. Any resemblance to actual persons, living or dead, or actual events is purely coincidental.</p>
      <p>No part of this book may be reproduced or transmitted in any form or by any means, electronic or mechanical, including photocopying, recording, or by any information storage and retrieval system, without the express written permission of the author.</p>`;return`${h}
<!DOCTYPE html>
<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops" lang="en">
<head>
  <meta charset="utf-8"/>
  <title>Copyright</title>
  <link rel="stylesheet" type="text/css" href="style.css"/>
</head>
<body>
  <section epub:type="copyright-page">
    <h2>${e.replace(/&/g,"&amp;").replace(/</g,"&lt;")}</h2>
    ${n}
  </section>
</body>
</html>`}function P(e){const t=e.map((i,o)=>{const n=i.title.replace(/&/g,"&amp;").replace(/</g,"&lt;");return`      <li><a href="chapters/c${o+1}.xhtml">${n}</a></li>`}).join(`
`);return`${h}
<!DOCTYPE html>
<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops" lang="en">
<head>
  <meta charset="utf-8"/>
  <title>Table of Contents</title>
  <link rel="stylesheet" type="text/css" href="style.css"/>
</head>
<body>
  <nav epub:type="toc" id="toc">
    <h2>Contents</h2>
    <ol>
      <li><a href="cover.xhtml">Cover</a></li>
      <li><a href="copyright.xhtml">Copyright</a></li>
${t}
    </ol>
  </nav>
</body>
</html>`}function S(e,t,i){const o=i.map((n,r)=>{const c=n.title.replace(/&/g,"&amp;").replace(/</g,"&lt;");return`    <navPoint id="ch${r+1}" playOrder="${r+3}">
      <navLabel><text>${c}</text></navLabel>
      <content src="chapters/c${r+1}.xhtml"/>
    </navPoint>`}).join(`
`);return`<?xml version="1.0" encoding="UTF-8"?>
<ncx xmlns="http://www.daisy.org/z3986/2005/ncx/" version="2005-1">
  <head>
    <meta name="dtb:uid" content="${t}"/>
    <meta name="dtb:depth" content="1"/>
    <meta name="dtb:totalPageCount" content="0"/>
    <meta name="dtb:maxPageNumber" content="0"/>
  </head>
  <docTitle><text>${e.replace(/&/g,"&amp;").replace(/</g,"&lt;")}</text></docTitle>
  <navMap>
    <navPoint id="cover" playOrder="1">
      <navLabel><text>Cover</text></navLabel>
      <content src="cover.xhtml"/>
    </navPoint>
    <navPoint id="copyright" playOrder="2">
      <navLabel><text>Copyright</text></navLabel>
      <content src="copyright.xhtml"/>
    </navPoint>
${o}
  </navMap>
</ncx>`}function L(e,t,i,o,n,r){const c=n.map((a,l)=>`    <item id="ch${l+1}" href="chapters/c${l+1}.xhtml" media-type="application/xhtml+xml"/>`).join(`
`),s=n.map((a,l)=>`    <itemref idref="ch${l+1}"/>`).join(`
`);return`<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="bookid" xml:lang="${i}">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:identifier id="bookid">${o}</dc:identifier>
    <dc:title>${e.replace(/&/g,"&amp;").replace(/</g,"&lt;")}</dc:title>
    <dc:creator>${t.replace(/&/g,"&amp;").replace(/</g,"&lt;")}</dc:creator>
    <dc:language>${i}</dc:language>
    <meta property="dcterms:modified">${C()}</meta>
    <meta name="cover" content="cover-image"/>
  </metadata>
  <manifest>
    <item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/>
    <item id="ncx" href="toc.ncx" media-type="application/x-dtbncx+xml"/>
    <item id="style" href="style.css" media-type="text/css"/>
    <item id="cover-image" href="${r.name}" media-type="${r.mime}" properties="cover-image"/>
    <item id="cover" href="cover.xhtml" media-type="application/xhtml+xml"/>
    <item id="copyright" href="copyright.xhtml" media-type="application/xhtml+xml"/>
${c}
  </manifest>
  <spine toc="ncx">
    <itemref idref="cover" linear="yes"/>
    <itemref idref="copyright" linear="yes"/>
${s}
    <itemref idref="nav" linear="no"/>
  </spine>
</package>`}const z=`<?xml version="1.0" encoding="UTF-8"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles>
    <rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/>
  </rootfiles>
</container>`,F=`body { font-family: Georgia, "Times New Roman", serif; line-height: 1.6; margin: 1em; }
h1, h2, h3 { font-family: Georgia, serif; }
h2.chapter-title { text-align: center; font-weight: bold; font-size: 1.4em; margin: 3em 0 2em; }
p { margin: 0; text-indent: 1.5em; }
p:first-child, h1 + p, h2 + p, h3 + p { text-indent: 0; }
blockquote { margin: 1.5em 2em; font-style: italic; }
blockquote[data-type="epigraph"] { text-align: center; margin: 2em 3em; }`;function A(e){return`urn:chronicler:${e.toLowerCase().replace(/[^a-z0-9]+/g,"-").replace(/^-|-$/g,"")||"untitled"}:${Date.now()}`}function f(e){return e.replace(/<[^>]*>/g,"").trim()}async function G(e){try{const t=await $(e);if(!t)return null;const i=await fetch(t);if(!i.ok)return null;const o=await i.arrayBuffer(),n=i.headers.get("content-type")||"image/jpeg",r=n.includes("png")?"png":n.includes("webp")?"webp":"jpg";return{bytes:o,mime:n,ext:r}}catch{return null}}async function Y(e,t,i={}){const o=f(e.author)||"Anonymous",n=f(e.title)||"Untitled",r="en",c=A(n),s=new u;s.file("mimetype","application/epub+zip",{compression:"STORE"}),s.file("META-INF/container.xml",z);const a=s.folder("OEBPS");let l;const m=!(i.coverSource==="generated")&&e.coverArt?await G(e.coverArt):null;if(m){const p=`cover.${m.ext}`;a.file(p,m.bytes),l={name:p,mime:m.mime}}else a.file("cover.svg",T(n,o)),l={name:"cover.svg",mime:"image/svg+xml"};a.file("style.css",F),a.file("cover.xhtml",E(l.name,n)),a.file("copyright.xhtml",O(n,o,i.copyrightNotice)),a.file("nav.xhtml",P(t)),a.file("toc.ncx",S(n,c,t)),a.file("content.opf",L(n,o,r,c,t,l));const g=a.folder("chapters");t.forEach((p,d)=>{g.file(`c${d+1}.xhtml`,k(p.title||`Chapter ${d+1}`,b(p.content||"")))});const x=await s.generateAsync({type:"blob",mimeType:"application/epub+zip"}),y=`${n.replace(/\s+/g,"_")||"Manuscript"}_${w()}.epub`;v.saveAs(x,y)}export{Y as exportToEpub};
