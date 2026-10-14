// The brand mark: a raspberry square with a rising trace. It is the one place
// the brand color appears without a state behind it.
export function Logo() {
  return (
    <span className="flex items-center gap-2">
      <svg viewBox="0 0 32 32" className="size-6" aria-hidden="true">
        <rect width="32" height="32" className="fill-brand" />
        <path
          d="M6 21l6-7 5 4 9-10"
          fill="none"
          className="stroke-brand-foreground"
          strokeWidth="3"
          strokeLinecap="square"
        />
      </svg>
      <span className="type-title" translate="no">
        piway
      </span>
    </span>
  )
}
