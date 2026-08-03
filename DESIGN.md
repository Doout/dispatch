---
name: Dispatch
description: An evidence-first deployment workbench for one private operator.
colors:
  graphite-rail: "#101b25"
  graphite-raised: "#1b2b38"
  mineral-canvas: "#edf1f4"
  evidence-white: "#fbfcfc"
  rule: "#c7d0d7"
  muted: "#5d6a75"
  signal-blue: "#075dcc"
  signal-blue-dark: "#064ca6"
  ready-green: "#08783d"
  exception-red: "#b4232d"
  amber-demo: "#a95b00"
typography:
  display:
    fontFamily: "Segoe UI Variable Text, Segoe UI, Arial, sans-serif"
    fontSize: "clamp(28px, 2.2vw, 38px)"
    fontWeight: 700
    lineHeight: 1
    letterSpacing: "-0.028em"
  body:
    fontFamily: "Segoe UI Variable Text, Segoe UI, Arial, sans-serif"
    fontSize: "14px"
    fontWeight: 400
    lineHeight: 1.55
  label:
    fontFamily: "Segoe UI Variable Text, Segoe UI, Arial, sans-serif"
    fontSize: "11px"
    fontWeight: 700
  evidence:
    fontFamily: "Cascadia Mono, SFMono-Regular, Consolas, monospace"
    fontSize: "10px"
    fontWeight: 600
    lineHeight: 1.55
rounded:
  stamp: "3px"
  control: "7px"
  record: "9px"
  round: "999px"
spacing:
  xs: "4px"
  sm: "8px"
  md: "14px"
  lg: "24px"
  xl: "28px"
components:
  button-primary:
    backgroundColor: "{colors.signal-blue}"
    textColor: "{colors.evidence-white}"
    rounded: "{rounded.control}"
    padding: "0 16px"
    height: "42px"
  field:
    backgroundColor: "{colors.evidence-white}"
    textColor: "{colors.graphite-rail}"
    rounded: "6px"
    padding: "0 11px"
    height: "44px"
  dispatch-record:
    backgroundColor: "{colors.evidence-white}"
    textColor: "{colors.graphite-rail}"
    rounded: "{rounded.record}"
    padding: "0 18px 0 12px"
---

# Design System: Dispatch

## Overview

**Creative North Star: "The Dispatch Workbench"**

Dispatch should feel like an operator's physical work surface translated into software: factual, dense enough for real work, and organized around movement records and their evidence. It uses calm mineral surfaces and a dark fixed rail so Signal Blue, Ready Green, and Exception Red remain meaningful.

The interface is operational rather than promotional. It never imitates a generic analytics dashboard, and it never hides source, target, state, or consequence behind decorative summaries.

**Key Characteristics:**

- Evidence-first and exceptions-first hierarchy
- Compact records with explicit handoff stages
- Reserved semantic color and mostly flat structural surfaces
- Responsive document flow with keyboard-visible controls

## Colors

Signal Blue owns selection and primary action. Ready Green and Exception Red appear only for real deployment states; Amber Demo labels non-production sample context. Graphite contains global navigation, while Mineral Canvas and Evidence White separate work from proof.

**The Semantic Signal Rule.** Never use green, red, or amber as decoration; each must communicate a current operational fact.

## Typography

The variable system sans keeps inventory readable at compact sizes. Monospace is reserved for commits, digests, timestamps, and log evidence.

**The Evidence Type Rule.** Use monospace only where exact character identity matters; application names, navigation, and actions remain in the system sans.

## Layout

Desktop uses a 218px rail, a fluid movement region, and a 420px evidence dossier. At 1180px these contract; below 960px the rail becomes an off-canvas control and the dossier follows the board in normal document flow. At 640px, records recompose into identity, result, and stage rows without horizontal scrolling. The minimum supported width is 320px.

## Elevation & Depth

Surfaces are flat by default. Borders establish the main structure; a restrained ambient shadow identifies selectable dispatch records and the active record gains a compact blue focus halo. The dossier uses only a faint directional separation from the board.

**The Structural Depth Rule.** Shadows identify interaction or selection, never decorate static containers.

## Shapes

Controls use gently compact corners, records use a slightly larger radius, state stamps stay nearly square, and status markers are circular. Thin rules carry hierarchy; strong colored rails are reserved for the state edge of the signature dispatch record.

## Components

### Buttons

Primary buttons are Signal Blue, compact, and confident; quiet buttons are pale with a neutral rule. All reach a 44px touch height on mobile and expose a visible blue focus halo.

### Inputs / Fields

Fields are Evidence White with a single neutral border and compact radius. Focus changes the border to Signal Blue and adds the shared halo; errors use the light Exception Red surface.

### Navigation

The Graphite Rail uses subdued items, a raised active row, and honest noninteractive “Planned” labels for unavailable destinations. The closed mobile rail is removed from focus and revealed by a labeled 48px menu control.

### Dispatch Record

The signature record binds application identity, immutable revision, target, four handoff stages, result, and relative time into one selectable row. Its narrow state edge and circular markers carry semantic status; the selected record uses the shared focus halo.

## Do's and Don'ts

### Do:

- **Do** put urgent and active records before completed history.
- **Do** keep source, commit, target, spec digest, state, and log evidence inspectable.
- **Do** preserve 44px mobile controls, visible keyboard focus, and reduced-motion behavior.

### Don't:

- **Don't** turn the workbench into a card-grid metrics dashboard.
- **Don't** use semantic status colors for visual variety.
- **Don't** create dead navigation or fixed mobile panels that obscure operator actions.
