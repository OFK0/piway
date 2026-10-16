import { IconContext } from '@phosphor-icons/react'
import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'

import App from '@/App'
import { ThemeProvider } from '@/components/theme-provider'
import { Toaster } from '@/components/ui/sonner'
import { TooltipProvider } from '@/components/ui/tooltip'
import { interceptLinks } from '@/lib/navigation'
import '@/index.css'

// Links between dashboard pages change the page without reloading it.
interceptLinks()

// A font is fetched when text first uses it. Start the code font now, not
// when the first card with code in it appears: text re-wraps when it
// arrives, and that shows as a layout shift.
for (const face of ['400 1rem "IBM Plex Mono"', '500 1rem "IBM Plex Mono"']) {
  document.fonts?.load(face).catch(() => {})
}

const iconDefaults = {
  color: 'currentColor',
  size: '1em',
  weight: 'regular',
  mirrored: false,
  'aria-hidden': true,
} as const

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <ThemeProvider>
      {/* Icons sit next to text that already says what they mean; icon-only
          buttons carry their own aria-label. The value replaces Phosphor's
          default context, so it must repeat the defaults (size above all). */}
      <IconContext.Provider value={iconDefaults}>
        <TooltipProvider>
          <App />
          <Toaster />
        </TooltipProvider>
      </IconContext.Provider>
    </ThemeProvider>
  </StrictMode>,
)
