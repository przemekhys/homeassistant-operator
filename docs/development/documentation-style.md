# Writing documentation

*For contributors — where a new page goes and what shape it takes. Not needed to use the operator.*

This documentation is organised by what the reader came to do, not by how much
they already know. Six sections, and every page belongs to exactly one of them.
Putting a page in the right one is most of the work; the rest is a template.

## Which section does my page go in?

Answer these in order and stop at the first "yes".

1. **Is it for people building the operator, not using it?**
   → `docs/development/`.
2. **Is it about a tool this project does not ship or control?**
   → `docs/ecosystem/`. See [the extra rules](#pages-about-third-party-tools).
3. **Would the reader be following it, start to finish, to learn the system?**
   → `docs/tutorials/`.
4. **Does the reader have a specific goal and already know the basics?**
   → `docs/how-to/`.
5. **Is the reader looking something up rather than reading?**
   → `docs/reference/`.
6. **Otherwise** — they want to understand something.
   → `docs/explanation/`.

The directory in the path must match the section. That way a misplaced page is
visible from its URL, without anyone reading it.

## The rule for mixed content

Most drafts are a mix. The rule that resolves it: **the type is decided by the
state the reader is in when they arrive, not by the subject matter.**

- They do not yet know they have a problem → explanation.
- They know what they want to achieve → how-to.
- They know exactly what they are looking for → reference.

Worked example. "Troubleshooting" looks like a catalogue, so it looks like
reference. But nobody browses it: they arrive with a symptom and a goal — make it
stop. It is a how-to guide, and it lives in `docs/how-to/`.

Second example. A page about backups will want to say *why* Home Assistant's own
backups do not protect you from losing the volume. That sentence is explanation
inside a how-to, and that is fine — a sentence of context is not a section. If it
grows past a short paragraph, cut it out into `docs/explanation/` and link to it.

## Page templates

Every page, whatever its type:

- exactly one `#` heading, at the top;
- directly underneath, one italic line declaring its type (the templates below);
- reachable from the navigation in `mkdocs.yml`;
- no links to files that are not in this repository (see [self-contained](#self-contained-content));
- code blocks that can be copied and run without editing, apart from clearly
  marked placeholders.

### Tutorial

```markdown
# <What the reader will have built>

*Tutorial — a guided walkthrough. Follow every step in order; you will end up with <result>.*

## Before you start
## Step 1 — …
## Where to go next
## Clean up
```

Make every choice for the reader and say that you made it. Every step ends with
something they can observe. The whole thing must be completable without opening
another page — links are for afterwards.

### How-to guide

```markdown
# <Goal, as a verb phrase>

*How-to — <goal>. Assumes you already have <starting state>.*

## Prerequisites
## <Steps, named after what they achieve>
## Verify
## Every field        ← link to the API reference, never a copied table
```

Title the page after the goal ("Expose an instance with TLS"), never after a
resource ("TLS"). No "How it works" section: if the mechanism matters, link to
the explanation.

### Reference

```markdown
# <What is catalogued>

*Reference — <what it catalogues>. Look things up here; it does not teach.*
```

Uniform entries, complete coverage, no narrative. A partial catalogue is worse
than none, because the reader cannot tell "this does not exist" from "we did not
finish writing". If the page is generated, say so and say from what.

### Explanation

```markdown
# <The idea>

*Explanation — why <mechanism> works the way it does. Nothing here needs a cluster.*
```

Name the alternative that was rejected and why. An explanation that only
describes what happens is a description; the "instead of what" is the part that
is actually useful. No step-by-step instructions — an illustrative snippet is
fine, a procedure is not.

### Contributor page

```markdown
# <Topic>

*For contributors — <what it covers>. Not needed to use the operator.*
```

Nothing a *user* needs may live only here.

## Pages about third-party tools

A page in `docs/ecosystem/` covers exactly one tool and must carry:

- the standard support-boundary admonition, verbatim from a neighbouring page;
- a **Tested with** line naming the tool, operator and Kubernetes versions you
  actually checked against;
- a **Security considerations** section, if anything in it acts without a human
  reviewing the change first;
- links to the operator's own how-to guides and reference rather than restating
  them.

The same two requirements — boundary note and tested versions — apply to a
third-party section embedded in a how-to page. The boundary belongs to the
content, not to its location.

Do not add a page for a tool nobody has tried. `docs/ecosystem/index.md` states
what belongs in that section; keep it accurate.

## Self-contained content

Anything published from this repository must make sense to someone who has only
this repository. A sentence pointing at internal planning notes, or citing a
numbered requirement from them, names a file that is not here and never will be —
it is a dead reference for every reader, and the gate below rejects it.

The same applies to the Go doc comments in `api/` — `crd-ref-docs` copies them
verbatim into the published API reference, so a dead pointer written there ships
exactly like one written in Markdown.

Write the reasoning where it belongs instead. `make docs-verify` fails the build
if it finds such a reference, and so does CI.

## Renaming or removing a page

Published URLs are quoted in released changelog entries, which are a historical
record and are not rewritten. So:

1. move the file;
2. add the old path to `redirect_maps` in `mkdocs.yml`;
3. fix every internal link — the strict build will tell you which.

Never delete a page without a redirect, and never leave a page in the navigation
with nothing in it.

## Checking your work

```sh
make docs-verify    # strict build: links, anchors, orphans + self-contained check
make docs-serve     # live preview at http://127.0.0.1:8000
```

The build is strict: a broken link, a broken anchor, or a page missing from the
navigation fails it. That is deliberate — those errors are cheap to fix now and
invisible once published.
