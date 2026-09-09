# Console design review, September 2026

The console uses a graphite navigation rail, signal blue selection, self-hosted Manrope, and consistent page headings. Status colors have operational meaning and always appear with text.

| Page | Review and changes |
| --- | --- |
| Deployments | Neutral stage summary, shared deployment list, readable revision metadata, expanded stage history |
| Deployment details | Summary, topology, values, and manifests; fixed a blank page when Docker returns null topology edges |
| Applications | Inventory, legacy collection URLs, creation menu and editor; corrected mobile secondary-text alignment; labelled resource and stage disclosure controls |
| Events | Rules and activity tabs, empty states, secondary calls to action |
| Projects | Summary counts, inventory, creation dialog |
| Servers | Target and relay counts, target table, creation dialog, topology title and refresh control |
| Secrets | Inventory and editor; bounded hidden file and radio inputs, visible keyboard focus |
| Connections | Provider chooser, GitHub App, secret-store, edge-node and Laneway forms |
| Access | Users, teams, sign-in methods, user editor; stacked mobile records replace wide tables |
| Sign-in | Graphite background, focused white form, shared typography |

Browser review uses an isolated demonstration controller and synthetic access and secret records. Production credentials and stored secrets are not used in screenshots. The route sweep covers 19 URLs at 1440px, 390px, and 320px, with additional form and populated-table checks. Provider authorization and deployment mutations are not submitted during visual review.

The frontend test suite includes a regression for null topology edges. The production build embeds the frontend and licensed font in the Go controller image.
