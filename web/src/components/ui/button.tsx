import { Button as ButtonPrimitive } from "@base-ui/react/button"
import { cva, type VariantProps } from "class-variance-authority"
import { cn } from "cn"

const buttonVariants = cva(
  "group/button inline-flex shrink-0 items-center justify-center rounded-none border border-transparent bg-clip-padding text-sm font-medium whitespace-nowrap transition-[color,background-color,border-color,opacity,box-shadow,transform] outline-none select-none active:not-aria-[haspopup]:translate-y-px disabled:cursor-not-allowed disabled:opacity-50 aria-invalid:border-destructive aria-invalid:ring-1 aria-invalid:ring-destructive/20 dark:aria-invalid:border-destructive/50 dark:aria-invalid:ring-destructive/40 [&_svg]:pointer-events-none [&_svg]:shrink-0 [&_svg:not([class*='size-'])]:size-4",
  {
    variants: {
      variant: {
        default: "bg-primary text-primary-foreground enabled:hover:bg-primary/85 disabled:border-border disabled:bg-muted disabled:text-muted-foreground disabled:opacity-100",
        brand: "bg-brand text-brand-foreground enabled:hover:bg-brand/90 disabled:border-border disabled:bg-muted disabled:text-muted-foreground disabled:opacity-100",
        outline:
          "border-border bg-background enabled:hover:bg-muted enabled:hover:text-foreground aria-expanded:bg-muted aria-expanded:text-foreground dark:border-input dark:bg-input/30 dark:enabled:hover:bg-input/50",
        secondary:
          "bg-secondary text-secondary-foreground enabled:hover:bg-[color-mix(in_oklch,var(--secondary),var(--foreground)_5%)] aria-expanded:bg-secondary aria-expanded:text-secondary-foreground disabled:border-border disabled:bg-muted disabled:text-muted-foreground disabled:opacity-100",
        ghost:
          "enabled:hover:bg-muted enabled:hover:text-foreground aria-expanded:bg-muted aria-expanded:text-foreground dark:enabled:hover:bg-muted/50",
        destructive:
          "bg-destructive/10 text-destructive enabled:hover:bg-destructive/20 focus-visible:border-destructive/40 focus-visible:ring-destructive/20 dark:bg-destructive/20 dark:enabled:hover:bg-destructive/30 dark:focus-visible:ring-destructive/40 disabled:border-border disabled:bg-muted disabled:text-muted-foreground disabled:opacity-100",
        link: "text-primary underline-offset-4 enabled:hover:underline",
      },
      size: {
        default:
          "h-9 gap-1.5 px-3 has-data-[icon=inline-end]:pr-2.5 has-data-[icon=inline-start]:pl-2",
        xs: "h-7 gap-1 rounded-none px-2 text-xs has-data-[icon=inline-end]:pr-1.5 has-data-[icon=inline-start]:pl-1.5 [&_svg:not([class*='size-'])]:size-3",
        sm: "h-8 gap-1 rounded-none px-2.5 text-[13px] has-data-[icon=inline-end]:pr-1.5 has-data-[icon=inline-start]:pl-1.5 [&_svg:not([class*='size-'])]:size-3.5",
        lg: "h-10 gap-2 px-4 has-data-[icon=inline-end]:pr-3 has-data-[icon=inline-start]:pl-3",
        icon: "size-9",
        "icon-xs": "size-7 rounded-none [&_svg:not([class*='size-'])]:size-3",
        "icon-sm": "size-8 rounded-none",
        "icon-lg": "size-10",
      },
    },
    defaultVariants: {
      variant: "default",
      size: "default",
    },
  }
)

function Button({
  className,
  variant = "default",
  size = "default",
  ...props
}: ButtonPrimitive.Props & VariantProps<typeof buttonVariants>) {
  return (
    <ButtonPrimitive
      data-slot="button"
      className={cn(buttonVariants({ variant, size, className }))}
      {...props}
    />
  )
}

export { Button, buttonVariants }
