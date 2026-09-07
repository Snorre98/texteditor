# ADR-0042: Tailwind CSS v4 + shadcn-vue (Reka UI) as the Tauri client's component system

Status: Accepted

Applies to: `client/tauri` only. The TUI stays OpenTUI-native (ADR-0023); no
other client or the engine is affected.

## Context

The Tauri client has zero styling infrastructure: hand-rolled scoped CSS per
component (`App.vue`, `Editor.vue`). The floating chat window (ADR-0041) needs
real UI machinery — message bubbles, scroll areas, collapsible panels,
selects, resize handles, tooltips, toasts — and reinventing those by hand with
plain CSS is exactly what ADR-0041 is trying to get away from. The user asked
for a component library, evaluated for chat.

Forces:

- The repo ethos is owned, readable code: the modules discipline (ADR-0016)
  favors source you can inspect and edit, and AI-navigability matters for
  maintenance.
- "No Node at runtime" (ADR-0013 §2) — any UI library is build-time only,
  which holds here.
- Clients stay dumb (ADR-0013 §3) — a component system is presentation-only
  and must not drag domain logic or an API layer with it.
- Accessibility: a chat window is keyboard/AT territory; hand-rolling
  focus/ARIA behavior for dialogs and collapsibles is high-risk.

## Decision

1. **Tailwind CSS v4** via the `@tailwindcss/vite` plugin; `@/*` path alias in
   `vite.config.ts` + `tsconfig.json`.
2. **shadcn-vue** as the component system: components are **copied into the
   repo** (`src/components/ui`) by the CLI, built on **Reka UI** (headless,
   accessible primitives) and Tailwind. New-york style, OKLCH theme variables,
   light/dark driven by `prefers-color-scheme` (the Tauri WebView).
3. **Add components as needed, never wholesale**: for ADR-0041 —
   `bubble` (Bubble/BubbleContent/BubbleGroup), `scroll-area`, `button`,
   `input`, `select`, `tooltip`, `collapsible`, `sonner` (toasts), `badge`.
   Runtime deps: `reka-ui`, `lucide-vue-next`, `clsx`, `tailwind-merge`,
   `class-variance-authority`, `tw-animate-css`, plus `marked` + `dompurify`
   for sanitized markdown rendering in chat bubbles.
4. **Coexistence boundary**: existing scoped CSS in `App.vue`/`Editor.vue`
   stays as-is; Tailwind applies to the new chat-window components. A later
   restyle of the editor chrome may migrate it, but that is out of scope here.
5. **No design-token coupling to the engine**: the theme is client-local CSS
   variables, consistent with ADR-0013 §3.

## Consequences

- **+** Owned, modifiable components — no black-box node_modules UI to fight.
- **+** Battle-tested a11y primitives (Reka UI) under every chat interaction.
- **+** A maintained design vocabulary for every future Tauri UI surface
  (ADR-0040's model selector, dialogs, the workspace sidebar).
- **−** First styling dependency in the client; Tailwind v4 is the entry point
  and stays pinned alongside the other toolchain locks.
- **−** Two styling systems coexist temporarily (scoped CSS + Tailwind) until
  the editor chrome migrates.

## Alternatives considered

- **Nuxt UI v4 (chat components)** — rejected: the strongest AI-chat offering
  in the Vue ecosystem, but Nuxt-first (color-mode, icons, module ecosystem);
  pulling it into a plain Vite + Tauri app is the wrong direction of coupling.
- **Hand-rolled everything + VueUse draggable** — rejected: least new deps, but
  rebuilds bubbles/resize/a11y that a component system provides; also does not
  answer the user's request for a component library.
- **PrimeVue / Vuetify** — rejected: no dedicated AI-chat components; heavy
  themed systems that fight a custom writing-app look.
- **vue-advanced-chat** — rejected: see ADR-0041 (chat-room model, ~500 kB).
