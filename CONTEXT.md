# PortaPixel context

The plan (`portapixel-dev-plan.md`, revision 5) is the intent. This file is the truth:
what we built, where we changed the plan, and the lessons that are not obvious from the
code. Read this file first. Then read `docs/ARCHITECTURE.md`.

Write this file in ASD-STE100 Simplified Technical English. Add an entry when you learn
something that cost you time. Remove an entry when it is no longer true.

## 1. How to work in this repository

- One Go module makes two binaries: `portapixeld` (device) and `portapixel-server`.
- `CGO_ENABLED=0` always. The web assets have no build step.
- `docs/ARCHITECTURE.md` is the contract for package names, paths and wire formats.
  Change the contract first, then the code.
- Run `gofmt -l .`, `go vet ./...` and `go test ./...` before each commit. The tests must
  pass on Windows and on Linux.
- Commit one logical unit of work at a time. Put a short entry in `CHANGELOG.md`.
- Standing review question: would a senior engineer say this is too complex?

## 2. Status

| Milestone | State |
|---|---|
| M0 bring-up spike | Done in QEMU only. No real hardware was available. See section 4. |
| v0.1 boot and play | In work |
| v0.2 the appliance | Not started |
| v0.3 the fleet | Not started |

## 3. Divergences from the plan

None yet.

## 4. M0 results

Not yet recorded.

## 5. Lessons

- The development machine is Windows. Linux-only code (statfs, DRM, CEC, mount) sits
  behind build tags or runtime checks, so that `go test ./...` runs on Windows.
