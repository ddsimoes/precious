import { Slot } from '@radix-ui/react-slot'
import { createContext, useContext, useId, type ComponentProps, type ReactNode } from 'react'

import { Label } from '@/components/ui/label'
import { cn } from '@/lib/utils'

interface FieldContext {
  controlId: string
  messageId: string
  error: ReactNode
}

const FieldContext = createContext<FieldContext | null>(null)

function useField(): FieldContext {
  const field = useContext(FieldContext)
  if (field === null) {
    throw new Error('form field parts must be rendered inside <FormField>')
  }
  return field
}

interface FormFieldProps extends ComponentProps<'div'> {
  // error, when set, marks the control invalid and is shown by <FormMessage>.
  error?: ReactNode
}

// FormField ties one label, one control, and its error message together
// through ids and ARIA attributes.
export function FormField({ error, className, ...props }: FormFieldProps) {
  const id = useId()
  const field: FieldContext = { controlId: `${id}-control`, messageId: `${id}-message`, error }
  return (
    <FieldContext value={field}>
      <div className={cn('grid gap-2', className)} {...props} />
    </FieldContext>
  )
}

export function FormLabel({ className, ...props }: ComponentProps<typeof Label>) {
  const { controlId, error } = useField()
  return (
    <Label
      htmlFor={controlId}
      className={cn(error ? 'text-destructive' : undefined, className)}
      {...props}
    />
  )
}

export function FormControl(props: ComponentProps<typeof Slot>) {
  const { controlId, messageId, error } = useField()
  return (
    <Slot
      id={controlId}
      aria-describedby={error ? messageId : undefined}
      aria-invalid={error ? true : undefined}
      {...props}
    />
  )
}

export function FormMessage({ className, ...props }: ComponentProps<'p'>) {
  const { messageId, error } = useField()
  if (!error) {
    return null
  }
  return (
    <p
      id={messageId}
      role="alert"
      className={cn('text-sm font-medium text-destructive', className)}
      {...props}
    >
      {error}
    </p>
  )
}
