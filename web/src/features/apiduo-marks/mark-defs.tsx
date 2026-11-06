/**
 * The SVG gradient every apiduo mark-stroked icon references.
 *
 * Lucide icons draw with `stroke="currentColor"`, which always ends up as one
 * flat colour — on the settings sidebar that colour sits one shade from the
 * selected state's, which is exactly the sameness the mark is trying to break.
 * Referencing a gradient instead gives the icon the same two-stop sweep as the
 * marked text, so icon and label read as one marked unit.
 *
 * SVG paint servers resolve against the document, not the element: the
 * `<defs>` block must exist exactly once somewhere in the tree. It is mounted
 * once in the authenticated layout, which wraps every surface that marks an
 * icon. The stops read the theme variables, so a deployment that re-tunes
 * `--primary` recolours the mark with it.
 */
export function ApiduoMarkDefs() {
  return (
    <svg
      aria-hidden='true'
      focusable='false'
      width='0'
      height='0'
      style={{ position: 'absolute' }}
    >
      <defs>
        <linearGradient id='apiduo-mark-grad' x1='0' y1='0' x2='1' y2='1'>
          <stop offset='0' style={{ stopColor: 'var(--apiduo-ink)' }} />
          <stop offset='1' style={{ stopColor: 'var(--primary)' }} />
        </linearGradient>
      </defs>
    </svg>
  )
}
