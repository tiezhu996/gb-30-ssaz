export type ApplicationStatus =
  | 'submitted'
  | 'org_review'
  | 'communicating'
  | 'confirmed'
  | 'offline_interview'
  | 'approved'
  | 'rejected'
  | 'withdrawn'

export const ApplicationStatusMap: Record<ApplicationStatus, { text: string; color: string }> = {
  submitted: { text: '已提交', color: 'blue' },
  org_review: { text: '机构审核中', color: 'gold' },
  communicating: { text: '沟通中', color: 'cyan' },
  confirmed: { text: '已确认', color: 'geekblue' },
  offline_interview: { text: '线下面签', color: 'purple' },
  approved: { text: '已通过', color: 'green' },
  rejected: { text: '已拒绝', color: 'red' },
  withdrawn: { text: '已撤回', color: 'default' },
}

// Statuses from which an applicant can still withdraw: everything before the
// offline interview.
export const WithdrawableStatuses: ApplicationStatus[] = [
  'submitted',
  'org_review',
  'communicating',
  'confirmed',
]

export function isWithdrawable(status: string): boolean {
  return (WithdrawableStatuses as string[]).includes(status)
}
