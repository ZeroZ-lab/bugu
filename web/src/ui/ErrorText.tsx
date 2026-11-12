// ErrorText 显示一段错误说明,没有内容时不占位置。
export function ErrorText({ message }: { message: string }) {
  return message ? (
    <p className="error" role="alert">
      {message}
    </p>
  ) : null;
}
