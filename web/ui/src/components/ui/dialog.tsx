import { useEffect, useId, useRef, type ReactNode } from 'react'

import { cn } from '@/lib/utils'

interface DialogProps {
  title: ReactNode
  description?: ReactNode
  // onClose is called when the owner dismisses the dialog with Escape.
  onClose: () => void
  // alert marks a dialog that asks to confirm a consequential action.
  alert?: boolean
  className?: string
  children: ReactNode
}

// Dialog is a modal dialog on the native <dialog> element, shown while it is
// rendered. The browser makes the rest of the page inert and returns focus
// when it closes; nothing injects styles, so the strict CSP holds.
export function Dialog({ title, description, onClose, alert = false, className, children }: DialogProps) {
  const ref = useRef<HTMLDialogElement>(null)
  const titleId = useId()
  const descriptionId = useId()

  useEffect(() => {
    const dialog = ref.current
    if (dialog !== null && !dialog.open) {
      dialog.showModal()
    }
  }, [])

  return (
    <dialog
      ref={ref}
      role={alert ? 'alertdialog' : undefined}
      aria-modal="true"
      aria-labelledby={titleId}
      aria-describedby={description === undefined ? undefined : descriptionId}
      onCancel={(event) => {
        event.preventDefault()
        onClose()
      }}
      className={cn(
        'm-auto w-full max-w-lg rounded-lg border bg-card p-6 text-card-foreground shadow-lg backdrop:bg-black/40',
        className,
      )}
    >
      <h2 id={titleId} className="text-lg font-semibold">
        {title}
      </h2>
      {description !== undefined && (
        <p id={descriptionId} className="mt-1 text-sm text-muted-foreground">
          {description}
        </p>
      )}
      <div className="mt-4 grid gap-4">{children}</div>
    </dialog>
  )
}
