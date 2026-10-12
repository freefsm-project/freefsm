# Legacy Office acceptance corpus

These documents are authored for FreeFSM tests (no third-party document data).
The flat OpenDocument XML sources describe actual paragraphs, worksheet cells,
and a presentation slide. LibreOffice exports them to real Word 97–2003 `.doc`,
Excel 97–2003 `.xls`, and PowerPoint 97–2003 `.ppt` compound binary documents.
The checked-in binaries let HTTP/browser tests run without an office runtime.

Regenerate from this directory with a private LibreOffice profile:

```sh
libreoffice -env:UserInstallation=file:///tmp/opencode/freefsm-46-office-profile --headless --convert-to 'doc:MS Word 97' --outdir . document.fodt
libreoffice -env:UserInstallation=file:///tmp/opencode/freefsm-46-office-profile --headless --convert-to 'xls:MS Excel 97' --outdir . workbook.fods
libreoffice -env:UserInstallation=file:///tmp/opencode/freefsm-46-office-profile --headless --convert-to 'ppt:MS PowerPoint 97' --outdir . slides.fodp
```

The corpus acceptance tests upload each real binary, reject each cross-format
extension permutation and text masquerading as Office, then verify authorized
downloads retain the exact original bytes. Browser coverage uses these same bytes.
Generated metadata may vary between regeneration runs; tests compare the committed
bytes, not a recomputed office-file encoding or detector-internal signatures.

This corpus was generated with LibreOffice **26.2.6.3**. Independent read-back
(`--convert-to fodt`, `fods`, `fodp` from the respective binaries) retained the
Word paragraphs and FILE-46 reference, Excel cells containing **46** and **2.5**,
and PowerPoint title and field-visit text. These are readable documents, not
magic-byte stubs or renamed text files.

Committed corpus SHA-256:

| Binary | SHA-256 |
|---|---|
| `document.doc` | `a80cfed59d6743d902691722f34fd1ce0b5c474ffc3739070e7869ee5bd19c9e` |
| `workbook.xls` | `256ae6f14c75e0df96d0709c7de17d16d6bcde8c79a41a8acb334c4e69a7b0e0` |
| `slides.ppt` | `77d94ac954863392e68b1bb54f3f8c7bb5edbf96233113668bfc5359ea9108a0` |
