import { cn } from "cn"

function Skeleton({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      data-slot="skeleton"
      className={cn("animate-pulse rounded-none bg-muted", className)}
      {...props}
    />
  )
}

// Stands in for a line of text that is still loading. The children are
// invisible placeholder text: they give the bar the width of a typical value,
// and the type style it inherits gives it the line height, so nothing moves
// when the real text replaces it. The bar covers the glyphs, not the leading.
function SkeletonText({ className, ...props }: React.ComponentProps<"span">) {
  return (
    <span
      data-slot="skeleton"
      aria-hidden="true"
      className={cn(
        "relative inline-block animate-pulse align-top text-transparent select-none before:absolute before:inset-x-0 before:inset-y-[0.2em] before:bg-muted",
        className
      )}
      {...props}
    />
  )
}

export { Skeleton, SkeletonText }
