// Choice 是一组横排的单选项,用原生 radio,方向键切换由浏览器处理。
export function Choice<T extends string>({
  name,
  value,
  options,
  onChange,
}: {
  name: string;
  value: T;
  options: readonly (readonly [T, string])[];
  onChange: (value: T) => void;
}) {
  return (
    <>
      {options.map(([v, label]) => (
        <label key={v} className="inline">
          <input type="radio" name={name} checked={value === v} onChange={() => onChange(v)} /> {label}
        </label>
      ))}
    </>
  );
}
