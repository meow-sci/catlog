import { twMerge } from 'tailwind-merge';

/**
 * Joins class names, letting later Tailwind utilities win over earlier ones of
 * the same kind. Without the merge, a `className` prop cannot override a
 * component's own padding — it just appends and the first one wins by specificity
 * accident.
 */
export function cn(...classes: (string | false | null | undefined)[]): string {
  return twMerge(classes.filter((c): c is string => typeof c === 'string' && c !== ''));
}
