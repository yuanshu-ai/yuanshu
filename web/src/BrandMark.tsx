export function BrandMark({ className = "" }: { className?: string }) {
  return <span className={`brand-mark ${className}`.trim()} aria-hidden="true"><img src="/brand/yuanshu-mark.svg" alt="" /></span>;
}
