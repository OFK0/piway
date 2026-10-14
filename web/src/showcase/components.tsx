import {
  ArrowClockwiseIcon,
  CpuIcon,
  MemoryIcon,
  DotsThreeIcon,
  PauseIcon,
  RocketLaunchIcon,
  ThermometerIcon,
  TrashIcon,
  WarningCircleIcon,
  WarningIcon,
} from '@phosphor-icons/react'
import { useState } from 'react'
import { toast } from 'sonner'

import { MetricCard, MetricCardSkeleton } from '@/components/metric-card'
import { StatusBadge } from '@/components/status-badge'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from '@/components/ui/dialog'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuShortcut,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { Empty, EmptyContent, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from '@/components/ui/empty'
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Kbd } from '@/components/ui/kbd'
import { Label } from '@/components/ui/label'
import { Progress } from '@/components/ui/progress'
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'
import { Spinner } from '@/components/ui/spinner'
import { Switch } from '@/components/ui/switch'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { Toggle } from '@/components/ui/toggle'
import { ToggleGroup, ToggleGroupItem } from '@/components/ui/toggle-group'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { Block, Section } from '@/showcase/section'
import { previewState, StateMatrix } from '@/showcase/state-matrix'

const buttonVariants = ['default', 'brand', 'outline', 'ghost', 'secondary', 'destructive', 'link'] as const
const intervals = [
  { label: '500 ms', value: '500ms' },
  { label: '1 second', value: '1s' },
  { label: '5 seconds', value: '5s' },
]

export function Components() {
  return (
    <Section
      id="components"
      title="Components"
      description="shadcn/ui components restyled from the tokens. States are pinned so they can be reviewed without interacting."
    >
      <Actions />
      <Inputs />
      <DataDisplay />
      <Feedback />
      <Overlays />
    </Section>
  )
}

function Actions() {
  return (
    <>
      <Block
        title="Button"
        description="One default button per group. Brand is for the single most important action on a screen, such as Deploy."
      >
        <StateMatrix
          rows={buttonVariants}
          states={['default', 'hover', 'focus', 'active', 'disabled', 'loading']}
          render={(variant, state) => (
            <Button
              variant={variant}
              {...(state === 'loading' ? { disabled: true } : previewState(state))}
            >
              {state === 'loading' && <Spinner data-icon="inline-start" />}
              {variant === 'destructive' ? 'Stop app' : variant === 'brand' ? 'Deploy' : 'Save'}
            </Button>
          )}
        />
      </Block>

      <div className="grid gap-6 lg:grid-cols-2">
        <Block title="Sizes" description="Controls share heights: 28, 32, 36 and 40px.">
          <div className="flex flex-wrap items-center gap-3">
            <Button size="xs">Extra small</Button>
            <Button size="sm">Small</Button>
            <Button>Default</Button>
            <Button size="lg">Large</Button>
          </div>
        </Block>
        <Block title="With icons" description="Icon-only buttons always have a label and a tooltip.">
          <div className="flex flex-wrap items-center gap-3">
            <Button>
              <RocketLaunchIcon data-icon="inline-start" />
              Deploy
            </Button>
            <Button variant="outline">
              <ArrowClockwiseIcon data-icon="inline-start" />
              Refresh
            </Button>
            <Tooltip>
              <TooltipTrigger render={<Button variant="ghost" size="icon" aria-label="More actions" />}>
                <DotsThreeIcon />
              </TooltipTrigger>
              <TooltipContent>More actions</TooltipContent>
            </Tooltip>
            <Button variant="outline" className="max-w-48">
              <span className="truncate">Restart the photo-gallery container</span>
            </Button>
          </div>
        </Block>
      </div>

      <div className="grid gap-6 lg:grid-cols-2">
        <Block title="Badge and status" description="Status is an icon, a word and a color. Normal shows nothing.">
          <div className="flex flex-wrap items-center gap-3">
            <Badge>Running</Badge>
            <Badge variant="secondary">v0.4.1</Badge>
            <Badge variant="outline">arm64</Badge>
            <StatusBadge status={{ level: 'warning', label: 'Warm' }} />
            <StatusBadge status={{ level: 'critical', label: 'Under-voltage' }} />
          </div>
        </Block>
        <Block title="Toggle group" description="For 2–5 mutually exclusive options, such as a time range.">
          <ToggleGroup defaultValue={['15m']} variant="outline" aria-label="Time range">
            <ToggleGroupItem value="5m">5 min</ToggleGroupItem>
            <ToggleGroupItem value="15m">15 min</ToggleGroupItem>
            <ToggleGroupItem value="1h">1 hour</ToggleGroupItem>
            <ToggleGroupItem value="24h">24 hours</ToggleGroupItem>
          </ToggleGroup>
          <div className="mt-4 flex flex-wrap items-center gap-4">
            <Toggle variant="outline" aria-label="Pause updates">
              <PauseIcon data-icon="inline-start" />
              Pause
            </Toggle>
            <div className="flex items-center gap-2">
              <Switch id="ds-live" defaultChecked />
              <Label htmlFor="ds-live">Live updates</Label>
            </div>
          </div>
          <p className="mt-4 text-muted-foreground">
            Press <Kbd>R</Kbd> to refresh.
          </p>
        </Block>
      </div>
    </>
  )
}

function Inputs() {
  const [interval, setInterval] = useState<string | null>('1s')
  return (
    <>
      <Block title="Input" description="Fields switch their border to the ring color on focus; invalid fields say what to fix.">
        <StateMatrix
          rows={['input'] as const}
          states={['default', 'hover', 'focus', 'disabled', 'invalid']}
          render={(_, state) => (
            <Input
              className="w-44"
              defaultValue={state === 'invalid' ? '1ms' : undefined}
              name="interval"
              autoComplete="off"
              spellCheck={false}
              placeholder="e.g. 2s…"
              aria-label="Collection interval"
              aria-invalid={state === 'invalid' || undefined}
              {...previewState(state)}
            />
          )}
        />
      </Block>

      <Block title="Form" description="A settings form built from Field, Input, Select and Switch.">
        <FieldGroup className="max-w-md">
          <Field>
            <FieldLabel htmlFor="ds-addr">Listen address</FieldLabel>
            <Input id="ds-addr" name="addr" autoComplete="off" spellCheck={false} defaultValue=":8080" />
            <FieldDescription>Use 127.0.0.1:8080 to accept local connections only.</FieldDescription>
          </Field>
          <Field data-invalid>
            <FieldLabel htmlFor="ds-queue">Stream queue</FieldLabel>
            <Input id="ds-queue" name="stream_queue" type="number" inputMode="numeric" min={1} autoComplete="off" defaultValue="0" aria-invalid />
            <FieldError>Must be at least 1.</FieldError>
          </Field>
          <Field>
            <FieldLabel htmlFor="ds-interval">Collection interval</FieldLabel>
            <Select items={intervals} value={interval} onValueChange={setInterval}>
              <SelectTrigger id="ds-interval" className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectGroup>
                  {intervals.map((i) => (
                    <SelectItem key={i.value} value={i.value}>
                      {i.label}
                    </SelectItem>
                  ))}
                </SelectGroup>
              </SelectContent>
            </Select>
          </Field>
          <Field orientation="horizontal">
            <Switch id="ds-alerts" defaultChecked />
            <FieldLabel htmlFor="ds-alerts">Notify me when the board throttles</FieldLabel>
          </Field>
          <Field orientation="horizontal" data-disabled>
            <Switch id="ds-tunnel" disabled />
            <FieldLabel htmlFor="ds-tunnel">Expose through a tunnel (coming in M5)</FieldLabel>
          </Field>
        </FieldGroup>
      </Block>
    </>
  )
}

function DataDisplay() {
  // The first card takes a new sample on demand to show the number motion.
  const [cpu, setCpu] = useState(12.4)
  return (
    <>
      <Block
        title="Metric card"
        description="Typical, critical, missing and loading. A missing value is a dash with a reason, never 0. A new value glides in."
      >
        <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
          <MetricCard title="CPU" icon={CpuIcon} value={cpu} unit="%" meter={cpu}>
            4 cores
          </MetricCard>
          <MetricCard
            title="Temperature"
            icon={ThermometerIcon}
            value={82.1}
            unit="°C"
            status={{ level: 'critical', label: 'Hot' }}
          >
            Throttling starts at 80 °C
          </MetricCard>
          <MetricCard
            title="CPU"
            icon={CpuIcon}
            value={null}
            missingReason="Needs a second sample. Refresh in a moment."
          />
          <MetricCardSkeleton title="Memory" icon={MemoryIcon} meter />
        </div>
        <Button variant="outline" className="mt-4 w-fit" onClick={() => setCpu(Math.round(Math.random() * 1000) / 10)}>
          Next sample
        </Button>
      </Block>

      <div className="grid gap-6 lg:grid-cols-2">
        <Block title="Table" description="Numbers right-aligned with tabular figures; identifiers in mono.">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Interface</TableHead>
                <TableHead className="text-right">Receive</TableHead>
                <TableHead className="text-right">Send</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              <TableRow>
                <TableCell className="type-code">eth0</TableCell>
                <TableCell className="text-right tabular-nums">1.2 MiB/s</TableCell>
                <TableCell className="text-right tabular-nums">84.0 KiB/s</TableCell>
              </TableRow>
              <TableRow>
                <TableCell className="type-code">wlan0</TableCell>
                <TableCell className="text-right tabular-nums">0 B/s</TableCell>
                <TableCell className="text-right tabular-nums">0 B/s</TableCell>
              </TableRow>
            </TableBody>
          </Table>
        </Block>
        <Block title="Tabs and progress">
          <Tabs defaultValue="cpu">
            <TabsList variant="line">
              <TabsTrigger value="cpu">CPU</TabsTrigger>
              <TabsTrigger value="memory">Memory</TabsTrigger>
              <TabsTrigger value="disk">Disk</TabsTrigger>
            </TabsList>
            <TabsContent value="cpu" className="pt-4">
              <Progress value={34} aria-label="CPU usage" />
            </TabsContent>
            <TabsContent value="memory" className="pt-4">
              <Progress value={62} aria-label="Memory usage" />
            </TabsContent>
            <TabsContent value="disk" className="pt-4">
              <Progress
                value={93}
                aria-label="Disk usage"
                className="**:data-[slot=progress-indicator]:bg-destructive"
              />
            </TabsContent>
          </Tabs>
          <div className="mt-6 flex flex-col gap-2">
            <Skeleton className="h-4 w-2/3" />
            <Skeleton className="h-4 w-1/2" />
          </div>
        </Block>
      </div>
    </>
  )
}

function Feedback() {
  return (
    <div className="grid gap-6 lg:grid-cols-2">
      <Block title="Alert" description="Critical alerts say what happened and what to do.">
        <div className="flex flex-col gap-4">
          <Alert variant="destructive">
            <WarningCircleIcon />
            <AlertTitle>Under-voltage detected</AlertTitle>
            <AlertDescription>The power supply cannot keep up. Use the official 5.1 V / 3 A supply.</AlertDescription>
          </Alert>
          <Alert>
            <WarningIcon />
            <AlertTitle>Some metrics could not be collected</AlertTitle>
            <AlertDescription>
              thermal: <code className="type-code">vcgencmd: executable file not found</code>
            </AlertDescription>
          </Alert>
          <div className="flex flex-wrap gap-3">
            <Button variant="outline" onClick={() => toast.success('Settings saved')}>
              Show a toast
            </Button>
            <Button
              variant="outline"
              onClick={() => toast.error("Couldn't restart photo-gallery", { description: 'The container exited with code 1.' })}
            >
              Show an error toast
            </Button>
          </div>
        </div>
      </Block>
      <Block title="Empty state" description="An empty screen invites the next action.">
        <Empty>
          <EmptyHeader>
            <EmptyMedia variant="icon">
              <RocketLaunchIcon />
            </EmptyMedia>
            <EmptyTitle>No apps yet</EmptyTitle>
            <EmptyDescription>Push a repository with a piway.yaml and it will appear here.</EmptyDescription>
          </EmptyHeader>
          <EmptyContent>
            <Button>Read the app guide</Button>
          </EmptyContent>
        </Empty>
      </Block>
    </div>
  )
}

function Overlays() {
  return (
    <Block title="Overlays" description="Menus and dialogs float with the one shadow; destructive actions always confirm.">
      <div className="flex flex-wrap items-center gap-3">
        <DropdownMenu>
          <DropdownMenuTrigger render={<Button variant="outline" />}>Open menu</DropdownMenuTrigger>
          <DropdownMenuContent className="min-w-48">
            <DropdownMenuGroup>
              <DropdownMenuItem>
                <ArrowClockwiseIcon />
                Restart
                <DropdownMenuShortcut>R</DropdownMenuShortcut>
              </DropdownMenuItem>
              <DropdownMenuItem>View logs</DropdownMenuItem>
            </DropdownMenuGroup>
            <DropdownMenuSeparator />
            <DropdownMenuGroup>
              <DropdownMenuItem variant="destructive">
                <TrashIcon />
                Delete app
              </DropdownMenuItem>
            </DropdownMenuGroup>
          </DropdownMenuContent>
        </DropdownMenu>

        <Dialog>
          <DialogTrigger render={<Button variant="destructive" />}>
            <TrashIcon data-icon="inline-start" />
            Delete app
          </DialogTrigger>
          <DialogContent>
            <DialogHeader>
              <DialogTitle>Delete photo-gallery?</DialogTitle>
              <DialogDescription>
                The container and its image are removed. Data in its volume is kept.
              </DialogDescription>
            </DialogHeader>
            <DialogFooter>
              <DialogClose render={<Button variant="outline" />}>Cancel</DialogClose>
              <DialogClose render={<Button variant="destructive" />}>Delete app</DialogClose>
            </DialogFooter>
          </DialogContent>
        </Dialog>

        <Tooltip>
          <TooltipTrigger render={<Button variant="ghost" />}>Hover for a tooltip</TooltipTrigger>
          <TooltipContent>Tooltips explain, they never hold the only copy of information.</TooltipContent>
        </Tooltip>
      </div>
    </Block>
  )
}
