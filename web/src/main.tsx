import { IconContext } from '@phosphor-icons/react'
import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'

import App from '@/App'
import { ThemeProvider } from '@/components/theme-provider'
import { Toaster } from '@/components/ui/sonner'
import { TooltipProvider } from '@/components/ui/tooltip'
import '@/index.css'

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <ThemeProvider>
      {/* Icons sit next to text that already says what they mean. Icon-only
          buttons carry their own aria-label. */}
      <IconContext.Provider value={{ 'aria-hidden': true }}>
        <TooltipProvider>
          <App />
          <Toaster />
        </TooltipProvider>
      </IconContext.Provider>
    </ThemeProvider>
  </StrictMode>,
)
