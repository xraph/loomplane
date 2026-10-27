/** The Loomplane logo, drawn from the shared #logo symbol. */
export function Wordmark({ className }: { className?: string }) {
  return (
    <svg
      className={className}
      viewBox="0 0 656.168 93.1"
      role="img"
      aria-label="Loomplane"
    >
      <use href="#logo" />
    </svg>
  );
}
