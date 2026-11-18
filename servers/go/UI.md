# Web UI settings

The browser UI (`/ui/`) has a settings panel (the gear button in the top bar)
for its look and feel. Settings are layered; later layers win:

1. **Built-in defaults** (also shipped as `ui/config.json`).
2. **Site defaults** from the administrator: the JSON file named by
   `ui_config` / `NEXUS_UI_CONFIG`, served as `/ui/config.json`. It needs no
   rebuild; edit the file and reload the page. Only the keys you list need to
   be present.
3. **The user's own choices** from the settings panel, kept in the browser
   (`localStorage`). *Reset to defaults* removes them.
4. **URL parameters** for one visit, e.g.
   `/ui/?theme=dark&fileList.maxRows=20&fileList.align=right`.
   Editing a setting in the panel replaces the URL value for that setting.

Unknown keys and values of the wrong type are ignored. *Copy as JSON* in the
panel copies the effective settings, ready to paste into the site file.

## Opening a file by link

`POST /view` and `POST /viewform` check an access token and then redirect to the
UI (see `AUTH.md`); they replace the old `GET /view?file=...` link. To open a
file without a token check (not possible in `file_system_mode`, where the UI only
works after `/view` or `/viewform`), use the UI's address directly:

```
http://host:8081/ui/#key=chess%2Fresults.nxs
http://host:8081/ui/#key=chess%2Fresults.nxs&path=saxs_azimuthal%2Fdata%2FI
```

`key` is the file's key as listed by `/files` (in `file_system_mode`, its
absolute path), and the optional `path` selects a group or field to show.

## Settings

| key | default | meaning |
|---|---|---|
| `theme` | `auto` | `auto` follows the operating system, or `light` / `dark` |
| `fontSize` | `14` | base font size in px (all text scales with it) |
| `sidebarWidth` | `340` | width of the tree panel in px (dragging the divider saves it) |
| `plotHeight` | `360` | height of line plots in px |
| `heatmapHeight` | `420` | height of heatmaps in px |
| `fileList.maxRows` | `12` | rows visible in the file dropdown before it scrolls |
| `fileList.width` | `input` | `input` (as wide as the box), `auto` (fit the longest key) or a CSS width such as `640px` |
| `fileList.align` | `left` | how the dropdown lines up with the box: `left` edge, `right` edge or `center` (it always stays inside the window) |
| `fileList.offsetX` | `0` | extra horizontal shift in px |
| `fileList.offsetY` | `4` | gap between the box and the dropdown in px (the list opens below the box; its height is limited to the room available) |
| `fileList.match` | `substring` | typing matches anywhere in the key, or only at its start (`prefix`) |
| `fileList.showSize` / `showDate` | `true` | columns shown for each file |
| `fileList.maxRender` | `500` | most entries drawn at once; type to narrow a longer list |

## File picker

Clicking the box selects the current name and shows **all** files; typing
filters them; the **x** button clears the box; the arrow button toggles the
list; Up/Down/Enter/Esc work from the keyboard. Enter on a key that is not in
the list still opens it.

## Example site file

```json
{
  "theme": "dark",
  "fontSize": 15,
  "plotHeight": 480,
  "fileList": { "maxRows": 20, "width": "auto", "align": "right" }
}
```
