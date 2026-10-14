import { cn } from "cn"
import { SpinnerIcon } from "@phosphor-icons/react"

// Unlike other icons, a spinner carries meaning on its own: it is a status
// announcement, so it opts out of the app-wide aria-hidden in IconContext.
function Spinner({ className, ...props }: React.ComponentProps<"svg">) {
  return (
    <SpinnerIcon
      data-slot="spinner"
      role="status"
      aria-label="Loading"
      aria-hidden={false}
      className={cn("size-4 animate-spin", className)}
      {...props}
    />
  )
}

export { Spinner }
