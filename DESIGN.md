---
name: Dispatch
description: A deployment control plane for private infrastructure.
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

# Dispatch design system

Dispatch is an operations interface. Put the current state, affected resource, and next action before explanatory text. Dense tables and topology views are useful when they expose real data. Empty decoration is not.

## Color

- Signal Blue marks selection, focus, links, and primary actions.
- Ready Green means a resource is ready or a run succeeded.
- Exception Red means a run failed or an action is destructive.
- Amber Demo marks sample data.
- Graphite is reserved for navigation and code surfaces.
- Mineral Canvas is the page background. Evidence White is the main content surface.

Do not use status colors as decoration. Pair each status color with text or an icon.

## Type

Use the system sans for navigation, application names, fields, and actions. Use monospace for values that require exact character matching, such as commits, digests, timestamps, paths, and logs.

## Layout

Wide screens use a 218px navigation rail and one content column. Detail views may add a 420px column when the list does not move or resize as selection changes.

Below 960px, move navigation off canvas and place details after the main content. Below 640px, stack record fields without horizontal scrolling. Support widths down to 320px.

Page content must use the shared maximum width. Forms, tables, empty states, and detail views must align to the same left and right edges.

## Depth

Use borders for structure. Add a small shadow only to menus, dialogs, or selectable records. A selected record may use a blue outline. Static containers do not need shadows.

## Controls

- Keep primary buttons blue and use one primary action per section.
- Use icon-only row actions when the icon is familiar. Provide a tooltip and accessible name.
- Put secondary actions in a three-dot menu when a row has more than two actions.
- Use custom menus and listboxes so focus, keyboard behavior, and placement match the rest of the interface.
- Keep touch targets at least 44px high on narrow screens.

## Fields

Fields use an Evidence White background, one neutral border, and a compact radius. Focus uses a Signal Blue border and the shared focus ring. Error text sits next to the field that needs attention.

Remove helper text when the label and placeholder already explain the field. Do not use placeholders as labels.

## Navigation and URLs

Every tab and selected record must have a stable URL. Back and forward navigation must restore the selected tab, expanded group, filters, and open detail page.

## Tables and records

Keep rows compact. Show only the columns needed to identify the item and its current state. Make the name open the detail view. Use the row action menu for edit, retry, pause, or delete.

Deployment records show the application, revision, target, result, and update time. Older failed attempts belong in history once a newer deployment succeeds.

## Dialogs

Dialogs must fit within the viewport and manage their own scroll area. Opening or closing a dialog must not move the underlying page. Long logs and manifests use tabs inside a fixed-size dialog or a dedicated URL.

## Runtime data

Topology nodes use short resource names and show the full name in a tooltip. Selecting a node opens its manifest, events, and logs when logs exist. Generated Kubernetes suffixes should not hide the workload name.

## Review checklist

- The primary action is clear.
- All boxes align to the shared content width.
- Empty space has a layout purpose.
- Text does not repeat labels or state.
- Keyboard focus is visible.
- Menus stay inside the viewport.
- Status does not rely on color alone.
- Narrow screens do not require horizontal scrolling.
